-- Migration 004 Down: Teardown labor efficiency targets and accounting periods

DROP TABLE IF EXISTS labor_efficiency_targets;
DROP TABLE IF EXISTS accounting_periods;
