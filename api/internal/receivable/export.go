package receivable

import (
	"bytes"
	"context"
	"fmt"

	"github.com/grahita/portal/api/internal/auth"
	"github.com/xuri/excelize/v2"
)

// ExportXLSX exports the same live balances displayed on the admin dashboard.
func (s *Service) ExportXLSX(ctx context.Context, actor auth.Actor) ([]byte, error) {
	if actor.Role != auth.RoleSuperadmin {
		return nil, ErrForbidden
	}
	overview, err := s.Overview(ctx, actor)
	if err != nil {
		return nil, err
	}
	f := excelize.NewFile()
	defer f.Close()
	const sheet = "Piutang Tempo"
	f.SetSheetName("Sheet1", sheet)
	heads := []string{"Cabang", "Periode", "Jatuh tempo", "Total", "Terbayar", "Sisa", "Status", "Umur (hari)"}
	for i, v := range heads {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellStr(sheet, cell, v); err != nil {
			return nil, err
		}
	}
	for i, row := range overview.Items {
		values := []any{row.BranchName, row.PeriodMonth, row.DueDate, row.Total, row.Paid, row.Remaining, row.Status, row.AgeDays}
		for col, value := range values {
			cell, _ := excelize.CoordinatesToCellName(col+1, i+2)
			if err := f.SetCellValue(sheet, cell, value); err != nil {
				return nil, fmt.Errorf("tulis %s: %w", cell, err)
			}
		}
	}
	_ = f.SetColWidth(sheet, "A", "A", 30)
	_ = f.SetColWidth(sheet, "B", "C", 16)
	_ = f.SetColWidth(sheet, "D", "F", 18)
	_ = f.SetColWidth(sheet, "G", "H", 20)
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1})
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return bytes.Clone(buf.Bytes()), nil
}
