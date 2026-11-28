-- Migration 004: Wedgewood 4-4-5 Accounting Periods and Labor Efficiency Targets

-- 1. Accounting Periods Table (4-4-5 Operational Calendar)
CREATE TABLE IF NOT EXISTS accounting_periods (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    year INTEGER NOT NULL,
    period_number INTEGER NOT NULL CHECK (period_number BETWEEN 1 AND 13),
    period_key VARCHAR(10) NOT NULL UNIQUE, -- e.g. '2026-11'
    month_name VARCHAR(20) NOT NULL,        -- 'November'
    start_date DATE NOT NULL,
    end_date DATE NOT NULL,
    weeks_count INTEGER NOT NULL CHECK (weeks_count IN (4, 5)),
    days_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (end_date > start_date)
);

CREATE INDEX IF NOT EXISTS ix_accounting_periods_dates ON accounting_periods(start_date, end_date);
CREATE INDEX IF NOT EXISTS ix_accounting_periods_year ON accounting_periods(year);

-- 2. Labor Efficiency Targets Table (Populated via Web CSV Upload)
CREATE TABLE IF NOT EXISTS labor_efficiency_targets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    venue_id VARCHAR(64) NOT NULL REFERENCES venues(id) ON DELETE CASCADE,
    venue_number VARCHAR(20),                -- Internal Finance property code (e.g. '214', '328')
    year INTEGER NOT NULL,
    period_key VARCHAR(10) NOT NULL,         -- e.g. '2026-11'
    foh_target NUMERIC(6, 2),
    boh_target NUMERIC(6, 2),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (venue_id, period_key)
);

CREATE INDEX IF NOT EXISTS ix_labor_efficiency_venue_year ON labor_efficiency_targets(venue_id, year);
CREATE INDEX IF NOT EXISTS ix_labor_efficiency_period ON labor_efficiency_targets(period_key);
CREATE INDEX IF NOT EXISTS ix_labor_efficiency_venue_number ON labor_efficiency_targets(venue_number);

