package mailer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestBuildRFC822TanpaLampiranMemakaiMultipartAlternative memastikan bentuk
// pesan sederhana tidak berubah.
func TestBuildRFC822TanpaLampiranMemakaiMultipartAlternative(t *testing.T) {
	raw := string(buildRFC822("no-reply@grahita.id", Message{
		To:      []string{"a@example.test"},
		Subject: "Halo",
		Text:    "teks biasa",
		HTML:    "<p>html</p>",
	}))
	for _, mau := range []string{
		"To: a@example.test\r\n",
		"Subject: Halo\r\n",
		"multipart/alternative",
		"text/plain; charset=utf-8",
		"text/html; charset=utf-8",
	} {
		if !strings.Contains(raw, mau) {
			t.Errorf("pesan harus memuat %q", mau)
		}
	}
	if strings.Contains(raw, "multipart/mixed") {
		t.Error("tanpa lampiran tidak boleh memakai multipart/mixed")
	}
}

// TestBuildRFC822DenganLampiran memastikan lampiran di-encode base64 dan
// diumumkan dengan nama serta tipe yang benar.
func TestBuildRFC822DenganLampiran(t *testing.T) {
	isi := []byte("PDF-ISI-SURAT-JALAN")
	raw := string(buildRFC822("no-reply@grahita.id", Message{
		To:      []string{"admin@example.test"},
		Cc:      []string{"lain@example.test"},
		Subject: "Surat Jalan — Cabang A", // non-ASCII
		Text:    "silakan unduh",
		HTML:    "<p>silakan unduh</p>",
		Attachments: []Attachment{
			{Name: "Surat-Jalan-1.pdf", ContentType: "application/pdf", Data: isi},
		},
	}))

	for _, mau := range []string{
		"multipart/mixed",
		"Content-Type: application/pdf; name=\"Surat-Jalan-1.pdf\"",
		"Content-Disposition: attachment; filename=\"Surat-Jalan-1.pdf\"",
		"Content-Transfer-Encoding: base64",
		"To: admin@example.test",
	} {
		if !strings.Contains(raw, mau) {
			t.Errorf("pesan harus memuat %q", mau)
		}
	}
	if !strings.Contains(raw, "Cc: lain@example.test") {
		t.Error("Cc harus ikut ditulis")
	}
	// Subjek non-ASCII di-encode sesuai RFC 2047.
	if !strings.Contains(raw, "Subject: =?utf-8?q?") {
		t.Errorf("subjek non-ASCII harus di-encode, dapat:\n%s", raw[:200])
	}
	// Isi lampiran bisa dipulihkan persis.
	enc := base64.StdEncoding.EncodeToString(isi)
	bersih := strings.ReplaceAll(raw, "\r\n", "")
	if !strings.Contains(bersih, enc) {
		t.Error("isi lampiran base64 tidak ditemukan utuh di pesan")
	}
}

// TestSMTPDeadlineTotalPeerDiam memastikan peer SMTP yang diam (menerima
// koneksi tetapi tidak pernah menjawab) tidak menggantung pemanggil: kirim
// berakhir dengan error dalam batas waktu, bukan selamanya.
func TestSMTPDeadlineTotalPeerDiam(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Terima koneksi lalu diam selamanya (tidak menulis apa pun).
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(30 * time.Second))
				// Hanya membaca, tidak membalas.
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	smtp := NewSMTP(host, port, "", "", "no-reply@grahita.id")
	smtp.SetTimeout(400 * time.Millisecond)

	selesai := make(chan error, 1)
	mulai := time.Now()
	go func() {
		selesai <- smtp.Send(context.Background(), Message{
			To: []string{"a@example.test"}, Subject: "diam", Text: "diam",
		})
	}()

	select {
	case err := <-selesai:
		if err == nil {
			t.Fatal("kirim ke peer diam harus gagal")
		}
		if durasi := time.Since(mulai); durasi > 5*time.Second {
			t.Fatalf("kirim harus gagal dalam batas waktu, butuh %v", durasi)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Send menggantung pada peer SMTP yang diam")
	}
}

// TestSMTPDeadlineMengikutiContextLebihKetat: deadline context yang lebih
// ketat tidak boleh dilonggarkan oleh timeout bawaan klien.
func TestSMTPDeadlineMengikutiContextLebihKetat(t *testing.T) {
	smtp := NewSMTP("127.0.0.1", 1, "", "", "no-reply@grahita.id")
	smtp.SetTimeout(time.Hour)

	ctx, batal := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer batal()
	_, batas, batalKonteks := smtp.deadline(ctx)
	defer batalKonteks()
	if sisa := time.Until(batas); sisa > time.Second {
		t.Fatalf("deadline context harus dipakai, sisa %v", sisa)
	}
}

// TestSMTPMengirimLampiranLewatServerPalsu menjalankan protokol SMTP sungguhan
// terhadap server palsu: pesan yang diterima harus memuat lampiran base64.
func TestSMTPMengirimLampiranLewatServerPalsu(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	pesan := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		r := bufio.NewReader(conn)
		tulis := func(s string) { _, _ = conn.Write([]byte(s)) }
		tulis("220 server-palsu ESMTP\r\n")
		var body strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			perintah := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(perintah, "EHLO"), strings.HasPrefix(perintah, "HELO"):
				tulis("250 server-palsu\r\n")
			case strings.HasPrefix(perintah, "MAIL FROM"), strings.HasPrefix(perintah, "RCPT TO"),
				strings.HasPrefix(perintah, "RSET"):
				tulis("250 OK\r\n")
			case perintah == "DATA":
				tulis("354 Mulai\r\n")
				for {
					baris, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(baris) == "." {
						break
					}
					body.WriteString(baris)
				}
				tulis("250 Diterima\r\n")
				pesan <- body.String()
			case perintah == "QUIT":
				tulis("221 Bye\r\n")
				return
			default:
				tulis("250 OK\r\n")
			}
		}
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	smtp := NewSMTP(host, port, "", "", "no-reply@grahita.id")
	smtp.SetTimeout(10 * time.Second)

	if err := smtp.Send(context.Background(), Message{
		To:      []string{"admin@example.test"},
		Subject: "Invoice",
		Text:    "lampiran menyusul",
		Attachments: []Attachment{
			{Name: "Invoice-1.pdf", ContentType: "application/pdf", Data: []byte("ISI-PDF")},
		},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case isi := <-pesan:
		if !strings.Contains(isi, "Invoice-1.pdf") {
			t.Errorf("pesan diterima tidak memuat nama lampiran:\n%s", isi)
		}
		if !strings.Contains(strings.ReplaceAll(isi, "\r\n", ""), base64.StdEncoding.EncodeToString([]byte("ISI-PDF"))) {
			t.Errorf("pesan diterima tidak memuat isi lampiran")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server palsu tidak menerima DATA")
	}
}

// TestLogMencatatLampiran memastikan mode tanpa SMTP tidak panik dengan
// lampiran.
func TestLogMencatatLampiran(t *testing.T) {
	if err := (Log{}).Send(context.Background(), Message{
		To: []string{"a@example.test"}, Subject: "x",
		Attachments: []Attachment{{Name: "a.pdf", Data: bytes.Repeat([]byte("x"), 3)}},
	}); err != nil {
		t.Fatalf("Log.Send: %v", err)
	}
}
