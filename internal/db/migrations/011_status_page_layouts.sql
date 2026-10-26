-- 011_status_page_layouts (SL-02): a status page chooses one of four
-- layouts (docs/12_STATUS_PAGES.md "Layouts"). Existing pages get sentence,
-- the default. Definition matches spec/schema.sql.

ALTER TABLE status_pages ADD COLUMN layout TEXT NOT NULL DEFAULT 'sentence'
  CHECK (layout IN ('signal','sentence','heartline','logbook'));
