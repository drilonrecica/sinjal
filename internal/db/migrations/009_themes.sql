-- 009_themes (M8-01): the three "Clear" themes replace the four first ones
-- (docs/05_THEMES.md): carbon and midnight become stark, paper becomes
-- daylight, terminal stays. Users gain the Ambient light preference.
--
-- users.theme gets its new CHECK without rebuilding the table: a rebuild
-- would DROP users inside this migration's transaction, where foreign keys
-- cannot be switched off, and the implicit delete would cascade to every
-- session and passkey. A new column, a copy, DROP COLUMN and RENAME COLUMN
-- leave the referencing tables alone. Both columns therefore move to
-- the end of their tables. Definitions match spec/schema.sql.

ALTER TABLE users ADD COLUMN theme_new TEXT CHECK (theme_new IN ('stark','terminal','daylight'));
UPDATE users SET theme_new = CASE theme
  WHEN 'paper' THEN 'daylight'
  WHEN 'terminal' THEN 'terminal'
  ELSE 'stark'
END WHERE theme IS NOT NULL;
ALTER TABLE users DROP COLUMN theme;
ALTER TABLE users RENAME COLUMN theme_new TO theme; -- NULL = instance default

ALTER TABLE users ADD COLUMN ambient_light INTEGER NOT NULL DEFAULT 1 CHECK (ambient_light IN (0,1));

-- status_pages.theme the same way, which also moves its default to daylight.
ALTER TABLE status_pages ADD COLUMN theme_new TEXT NOT NULL DEFAULT 'daylight';
UPDATE status_pages SET theme_new = CASE theme
  WHEN 'paper' THEN 'daylight'
  WHEN 'terminal' THEN 'terminal'
  ELSE 'stark'
END;
ALTER TABLE status_pages DROP COLUMN theme;
ALTER TABLE status_pages RENAME COLUMN theme_new TO theme;
