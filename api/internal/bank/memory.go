package bank

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/grahita/portal/api/internal/audit"
	"github.com/grahita/portal/api/internal/auth"
)

type memoryBranch struct {
	active  bool
	version int64
}

// Memory adalah implementasi Repository in-memory untuk pengujian.
type Memory struct {
	mu          sync.Mutex
	rows        []Account
	audit       []audit.Entry
	seq         int
	branches    map[string]memoryBranch
	assignments map[string]map[string]bool
}

func NewMemory() *Memory {
	return &Memory{branches: map[string]memoryBranch{}, assignments: map[string]map[string]bool{}}
}

func (m *Memory) PutBranch(id string, active bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.branches[id] = memoryBranch{active: active}
}

func (m *Memory) Assign(branchID, accountID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.assignments[branchID] == nil {
		m.assignments[branchID] = map[string]bool{}
	}
	m.assignments[branchID][accountID] = true
}

func (m *Memory) Put(a Account) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.ID == "" {
		m.seq++
		a.ID = fmt.Sprintf("bk-%d", m.seq)
	}
	m.rows = append(m.rows, a)
}

func (m *Memory) AuditEntries() []audit.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]audit.Entry, len(m.audit))
	copy(out, m.audit)
	return out
}

func (m *Memory) List(context.Context) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]Account(nil), m.rows...)
	sortAccounts(out)
	return out, nil
}

func (m *Memory) ListActive(context.Context) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Account, 0, len(m.rows))
	for _, a := range m.rows {
		if a.IsActive {
			out = append(out, a)
		}
	}
	sortAccounts(out)
	return out, nil
}

func (m *Memory) Get(_ context.Context, id string) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.rows {
		if a.ID == id {
			return a, nil
		}
	}
	return Account{}, ErrNotFound
}

func (m *Memory) Create(_ context.Context, in Account, rec audit.Entry) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.duplicate(in, "") {
		return Account{}, ErrDuplicate
	}
	m.seq++
	in.ID = fmt.Sprintf("bk-%d", m.seq)
	m.rows = append(m.rows, in)
	rec.EntityID = in.ID
	rec.After = accountAuditSnapshot(in)
	m.audit = append(m.audit, rec)
	return in, nil
}

func (m *Memory) Update(_ context.Context, next Account, rec audit.Entry) (Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.rows {
		if a.ID != next.ID {
			continue
		}
		if m.duplicate(next, next.ID) {
			return Account{}, ErrDuplicate
		}
		statusChanged := a.IsActive != next.IsActive
		m.rows[i] = next
		m.audit = append(m.audit, rec)
		if statusChanged {
			for branchID, assigned := range m.assignments {
				if assigned[next.ID] {
					b := m.branches[branchID]
					b.version++
					m.branches[branchID] = b
				}
			}
		}
		return next, nil
	}
	return Account{}, ErrNotFound
}

func (m *Memory) EffectiveForBranch(_ context.Context, branchID string) (EffectiveSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.branches[branchID]
	if !ok {
		return EffectiveSet{}, ErrBranchNotFound
	}
	out := EffectiveSet{Accounts: []Account{}, Version: b.version}
	if !b.active {
		return out, nil
	}
	for _, a := range m.rows {
		if a.IsActive && m.assignments[branchID][a.ID] {
			out.Accounts = append(out.Accounts, a)
		}
	}
	sortAccounts(out.Accounts)
	return out, nil
}

func (m *Memory) ListAssignments(_ context.Context, branchID string) (AssignmentList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.branches[branchID]
	if !ok {
		return AssignmentList{}, ErrBranchNotFound
	}
	out := AssignmentList{BranchID: branchID, Version: b.version, Items: []AssignmentItem{}}
	for _, a := range m.rows {
		allowed := m.assignments[branchID][a.ID]
		out.Items = append(out.Items, AssignmentItem{
			ID: a.ID, Bank: a.Bank, NamaPemilik: a.NamaPemilik, DisplayNomor: a.DisplayNomor(),
			IsActive: a.IsActive, IsAllowed: allowed, Dormant: allowed && (!a.IsActive || !b.active),
			Effective: allowed && a.IsActive && b.active,
		})
	}
	sort.Slice(out.Items, func(i, j int) bool {
		if out.Items[i].Bank != out.Items[j].Bank {
			return out.Items[i].Bank < out.Items[j].Bank
		}
		return out.Items[i].ID < out.Items[j].ID
	})
	return out, nil
}

func (m *Memory) ReplaceAssignments(ctx context.Context, branchID string, in ReplaceInput, actor auth.Actor) (ReplaceResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.branches[branchID]
	if !ok {
		return ReplaceResult{}, ErrBranchNotFound
	}
	if b.version != in.ExpectedVersion {
		return ReplaceResult{}, ErrVersionConflict
	}
	desired := make(map[string]bool, len(in.BankAccountIDs))
	for _, id := range in.BankAccountIDs {
		var found *Account
		for i := range m.rows {
			if m.rows[i].ID == id {
				found = &m.rows[i]
				break
			}
		}
		if found == nil {
			return ReplaceResult{}, ErrNotFound
		}
		if !found.IsActive && !m.assignments[branchID][id] {
			return ReplaceResult{}, ErrAccountInactive
		}
		desired[id] = true
	}
	current := m.assignments[branchID]
	changed := len(desired) != len(current)
	if !changed {
		for id := range desired {
			if !current[id] {
				changed = true
				break
			}
		}
	}
	if changed {
		before := sortedKeys(current)
		m.assignments[branchID] = desired
		b.version++
		m.branches[branchID] = b
		m.audit = append(m.audit, audit.NewEntry(ctx, actor, "branch_bank_allowlist", branchID, "replace",
			map[string]any{"version": in.ExpectedVersion, "bank_account_ids": before},
			map[string]any{"version": b.version, "bank_account_ids": in.BankAccountIDs}))
	}
	available := false
	for _, a := range m.rows {
		if desired[a.ID] && a.IsActive && b.active {
			available = true
		}
	}
	return ReplaceResult{BranchID: branchID, Version: b.version, BankAccountIDs: in.BankAccountIDs,
		PaymentAvailable: available, Changed: changed}, nil
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for id, enabled := range values {
		if enabled {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Memory) duplicate(a Account, exceptID string) bool {
	for _, other := range m.rows {
		if other.ID != exceptID && other.Bank == a.Bank && other.Nomor == a.Nomor {
			return true
		}
	}
	return false
}

func sortAccounts(rows []Account) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Bank != rows[j].Bank {
			return rows[i].Bank < rows[j].Bank
		}
		return rows[i].ID < rows[j].ID
	})
}
