-- DESTRUCTIVE: removes open issue tracking and latest resolution details.
-- Roll back application code first. Export this table before executing down.
-- Historical resolution decisions in audit_logs are deliberately retained.
DROP TABLE unresolved_sync_records;
