package masterimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/unicode/norm"

	"github.com/grahita/portal/api/internal/tarif"
)

type ExcelParser struct {
	expectedHash string
}

func NewExcelParser(expectedHash string) *ExcelParser {
	if expectedHash == "" {
		expectedHash = ExpectedHash
	}
	return &ExcelParser{expectedHash: strings.ToLower(expectedHash)}
}

type productSpec struct {
	Name       string
	Code       string
	Start      int
	FirstLabel string
}

var productSpecs = []productSpec{
	{"SETORAN KLASIKAL LENGKAP", "setoran-klasikal-lengkap", 4, "HJM"},
	{"IQ12", "iq12", 7, "HJM"},
	{"KHUSUS - PPM", "ppm", 10, "PPM"},
	{"KHUSUS - IDV", "idv", 13, "IDV"},
	{"KHUSUS - MUKPB", "mukpb", 16, "MUKPB"},
	{"KHUSUS - AU", "au", 19, "AU"},
	{"KHUSUS - MGRPLUS", "mgrplus", 22, "MGRPLUS"},
	{"KHUSUS - CSR", "csr", 25, "HJM"},
	{"EXPRES - KLASIKAL", "expres-klasikal", 28, "KLASIKAL"},
	{"EXPRES - IDV", "expres-idv", 31, "IDV"},
	{"EXPRES - S.EXPRES", "s-expres", 34, "S.EXPRES"},
	{"PTSD", "ptsd", 37, "PTSD"},
	{"KLINIS", "klinis", 40, "KLINIS"},
}

var nonAlnum = regexp.MustCompile(`[^\pL\pN]+`)

func normalizeKey(v string) string {
	v = strings.ToLower(norm.NFKC.String(strings.TrimSpace(v)))
	v = nonAlnum.ReplaceAllString(v, "-")
	return strings.Trim(v, "-")
}

// NormalizeSourceName membentuk bagian key sumber yang stabil. Repository
// memakainya hanya sebagai fallback untuk data legacy yang belum mempunyai
// persisted source mapping.
func NormalizeSourceName(v string) string { return normalizeKey(v) }

func fingerprint(parts ...string) string {
	return tarif.Sidik(parts...)
}

func (p *ExcelParser) Parse(r io.Reader, declaredSize int64) (ParsedWorkbook, error) {
	if declaredSize > MaxWorkbookSize {
		return ParsedWorkbook{}, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxWorkbookSize+1))
	if err != nil {
		return ParsedWorkbook{}, fmt.Errorf("baca workbook: %w", err)
	}
	if len(data) > MaxWorkbookSize {
		return ParsedWorkbook{}, ErrTooLarge
	}
	if len(data) < 4 || !bytes.Equal(data[:2], []byte("PK")) {
		return ParsedWorkbook{}, ErrInvalidWorkbook
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if p.expectedHash != "" && hash != p.expectedHash {
		return ParsedWorkbook{}, fmt.Errorf("%w: got %s", ErrHashMismatch, hash)
	}

	book, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return ParsedWorkbook{}, fmt.Errorf("%w: %v", ErrInvalidWorkbook, err)
	}
	defer func() { _ = book.Close() }()
	found := false
	for _, name := range book.GetSheetList() {
		if name == SheetName {
			found = true
			break
		}
	}
	if !found {
		return ParsedWorkbook{}, fmt.Errorf("%w: sheet_missing", ErrStructure)
	}
	dim, err := book.GetSheetDimension(SheetName)
	if err != nil || dim != "A1:AV36" {
		return ParsedWorkbook{}, fmt.Errorf("%w: dimension=%s", ErrStructure, dim)
	}
	rows, err := book.GetRows(SheetName, excelize.Options{RawCellValue: true})
	if err != nil {
		return ParsedWorkbook{}, fmt.Errorf("%w: baca sheet: %v", ErrInvalidWorkbook, err)
	}
	if len(rows) != 36 {
		return ParsedWorkbook{}, fmt.Errorf("%w: rows=%d", ErrStructure, len(rows))
	}
	if err := validateHeaders(rows); err != nil {
		return ParsedWorkbook{}, err
	}

	out := ParsedWorkbook{SourceHash: hash, Branches: make([]SourceBranch, 0, ExpectedRows)}
	seen := make(map[string]bool, ExpectedRows)
	for i := 2; i < 36; i++ {
		row := padded(rows[i], 48)
		if strings.TrimSpace(row[0]) == "" || strings.TrimSpace(row[1]) == "" {
			return ParsedWorkbook{}, fmt.Errorf("%w: required_cell row=%d", ErrStructure, i+1)
		}
		key := normalizeKey(row[1])
		if key == "" || seen[key] {
			return ParsedWorkbook{}, fmt.Errorf("%w: duplicate_branch row=%d", ErrInvalidValue, i+1)
		}
		seen[key] = true
		b := SourceBranch{
			Row: i + 1, SourceKey: "branch:" + key, Name: cleanText(row[1]), Position: cleanText(row[2]),
			Tariffs: make(map[string]MoneyComponent, len(productSpecs)),
		}
		b.Fingerprint = fingerprint(b.SourceKey)
		if head := cleanText(row[3]); head != "" {
			b.HeadName = &head
		}
		for _, spec := range productSpecs {
			component, err := parseComponent(row[spec.Start], row[spec.Start+1], row[spec.Start+2])
			if err != nil {
				return ParsedWorkbook{}, fmt.Errorf("%w: row=%d field=%s", err, i+1, spec.Code)
			}
			b.Tariffs[spec.Code] = component
		}
		for c := 43; c < 48; c++ {
			if strings.TrimSpace(row[c]) == "" {
				continue
			}
			col, _ := excelize.ColumnNumberToName(c + 1)
			b.ReviewCells = append(b.ReviewCells, ReviewCell{
				Column: col, Fingerprint: fingerprint(strconv.Itoa(i+1), col, row[c]), Value: cleanText(row[c]),
			})
		}
		out.Branches = append(out.Branches, b)
	}
	if len(out.Branches) != ExpectedRows {
		return ParsedWorkbook{}, fmt.Errorf("%w: branches=%d", ErrStructure, len(out.Branches))
	}
	return out, nil
}

func padded(row []string, n int) []string {
	if len(row) >= n {
		return row[:n]
	}
	out := make([]string, n)
	copy(out, row)
	return out
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(s)), " ")
}

func validateHeaders(rows [][]string) error {
	if len(rows) < 2 {
		return fmt.Errorf("%w: header_missing", ErrStructure)
	}
	h1, h2 := padded(rows[0], 48), padded(rows[1], 48)
	expected1 := make([]string, 48)
	expected1[0] = "IDENTITAS CABANG"
	for _, spec := range productSpecs {
		expected1[spec.Start] = spec.Name
	}
	expected1[43] = "CATATAN KHUSUS"
	expected2 := make([]string, 48)
	copy(expected2, []string{"NO", "CABANG", "JABATAN", "KEPALA CABANG"})
	for _, spec := range productSpecs {
		expected2[spec.Start], expected2[spec.Start+1], expected2[spec.Start+2] = spec.FirstLabel, "P1", "SPV"
	}
	expected2[43], expected2[44], expected2[45] = "NOTED 1", "NOTED 2", "NOTED 3"
	for i := 0; i < 48; i++ {
		if cleanText(h1[i]) != expected1[i] || cleanText(h2[i]) != expected2[i] {
			col, _ := excelize.ColumnNumberToName(i + 1)
			return fmt.Errorf("%w: header_changed column=%s", ErrStructure, col)
		}
	}
	return nil
}

func parseComponent(hjmText, p1Text, spvText string) (MoneyComponent, error) {
	hjmText, p1Text, spvText = strings.TrimSpace(hjmText), strings.TrimSpace(p1Text), strings.TrimSpace(spvText)
	if hjmText == "" {
		return MoneyComponent{Available: false, Fingerprint: fingerprint("unavailable", p1Text, spvText)}, nil
	}
	hjm, err := parseRupiah(hjmText)
	if err != nil {
		return MoneyComponent{}, err
	}
	spv := int64(0)
	if spvText != "" {
		spv, err = parseRupiah(spvText)
		if err != nil {
			return MoneyComponent{}, err
		}
	}
	p1, err := tarif.ParseRasio(p1Text)
	if err != nil {
		return MoneyComponent{}, ErrInvalidValue
	}
	final, ok := tarif.Final(hjm, p1, spv)
	if !ok {
		return MoneyComponent{}, ErrInvalidValue
	}
	return MoneyComponent{
		Available: true, HJM: hjm, P1: p1.RatString(), SPV: spv, FinalItem: final,
		Fingerprint: tarif.SidikTersedia(hjm, p1, spv, final),
	}, nil
}

func parseRupiah(v string) (int64, error) {
	if strings.ContainsAny(v, ",") {
		return 0, ErrInvalidValue
	}
	r := new(big.Rat)
	if _, ok := r.SetString(v); !ok || !r.IsInt() || r.Sign() < 0 {
		return 0, ErrInvalidValue
	}
	if !r.Num().IsInt64() {
		return 0, ErrInvalidValue
	}
	return r.Num().Int64(), nil
}

func maskedTextFingerprint(v string) string {
	return fingerprint(cleanText(v))
}

func isTextComplete(v *string) bool {
	return v != nil && strings.TrimFunc(*v, unicode.IsSpace) != ""
}
