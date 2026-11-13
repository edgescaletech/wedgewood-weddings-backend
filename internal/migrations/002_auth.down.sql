-- Rolling back authentication permanently deletes local users and sessions.
-- Back up the database and confirm these tables were introduced by migration 002.
DROP TABLE IF EXISTS auth_sessions;
DROP TABLE IF EXISTS users;
