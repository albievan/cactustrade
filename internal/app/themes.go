package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/albievan/cactustrade/internal/domain"
)

// Themes lists the current account's themes (view).
func (s *Service) Themes(ctx context.Context, p Principal) ([]domain.Theme, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return nil, err
	}
	return s.d.Store.Themes().List(ctx, v.ID)
}

// cachePicked makes sure every picked share is in the instrument cache (eligibility is
// checked when the scanner reviews it).
func (s *Service) cachePicked(ctx context.Context, v domain.AccountView, ids []domain.InstrumentID) error {
	var missing []domain.InstrumentID
	for _, id := range ids {
		if _, _, err := s.d.Store.Instruments().Get(ctx, id); err != nil {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := s.needBroker(); err != nil {
		return err
	}
	ins, err := s.d.Market.Instruments(brokerCtx(ctx, v), missing)
	if err != nil {
		return fmt.Errorf("looking up the picked shares: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	for _, id := range missing {
		i := slices.IndexFunc(ins, func(in domain.Instrument) bool { return in.ID == id })
		if i < 0 {
			return domain.Invalid("instruments", fmt.Sprintf("eToro does not know instrument %d", id))
		}
		if !domain.IsShare(ins[i].Type) || ins[i].Market == "" {
			return domain.Invalid("instruments", ins[i].Symbol+" is not a share on LSE or NYSE/NASDAQ")
		}
		if err := s.d.Store.Instruments().Upsert(ctx, ins[i], domain.Eligibility{Instrument: id, Symbol: ins[i].Symbol}, now); err != nil {
			return err
		}
	}
	return nil
}

// SaveTheme creates (ID "") or updates a theme of the current account (trade access).
func (s *Service) SaveTheme(ctx context.Context, p Principal, t domain.Theme) (domain.Theme, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return domain.Theme{}, err
	}
	if t, err = domain.NormalizeTheme(t); err != nil {
		return domain.Theme{}, err
	}
	if err := s.cachePicked(ctx, v, t.Instruments); err != nil {
		return domain.Theme{}, err
	}
	now := s.d.Clock.Now().UTC()
	t.AccountID, t.UpdatedAt = v.ID, now
	if t.ID == "" {
		t.ID, t.CreatedAt = s.d.Random.ID(), now
		if err := s.d.Store.Themes().Create(ctx, t); err != nil {
			return domain.Theme{}, fmt.Errorf("app.SaveTheme: %w", err)
		}
		s.audit(ctx, p, "theme.create", t.ID, t.Name)
		s.emitFor(ctx, v.ID, p.Username, "theme.created", domain.SeverityInfo, "Theme "+t.Name+" added", "")
		return t, nil
	}
	old, err := s.d.Store.Themes().Get(ctx, t.ID)
	if err != nil || old.AccountID != v.ID {
		return domain.Theme{}, fmt.Errorf("theme: %w", domain.ErrNotFound)
	}
	t.CreatedAt = old.CreatedAt
	if err := s.d.Store.Themes().Update(ctx, t); err != nil {
		return domain.Theme{}, fmt.Errorf("app.SaveTheme: %w", err)
	}
	s.audit(ctx, p, "theme.update", t.ID, t.Name)
	return t, nil
}

// DeleteTheme removes a theme of the current account (trade access).
func (s *Service) DeleteTheme(ctx context.Context, p Principal, id string) error {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return err
	}
	t, err := s.d.Store.Themes().Get(ctx, id)
	if err != nil || t.AccountID != v.ID {
		return fmt.Errorf("theme: %w", domain.ErrNotFound)
	}
	if err := s.d.Store.Themes().Delete(ctx, id); err != nil {
		return fmt.Errorf("app.DeleteTheme: %w", err)
	}
	s.audit(ctx, p, "theme.delete", id, t.Name)
	return nil
}

// Industries lists eToro's stock industries (view; read with the current account's keys).
func (s *Service) Industries(ctx context.Context, p Principal) ([]domain.Industry, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return nil, err
	}
	if s.d.Research == nil {
		return nil, fmt.Errorf("industries are not available: %w", domain.ErrUnavailable)
	}
	inds, err := s.d.Research.Industries(brokerCtx(ctx, v))
	if err != nil {
		return nil, fmt.Errorf("app.Industries: %w", err)
	}
	return inds, nil
}
