-- Local dev only (down migrations never ship in the image).
ALTER TABLE memory_items DROP COLUMN IF EXISTS embedding_model;
