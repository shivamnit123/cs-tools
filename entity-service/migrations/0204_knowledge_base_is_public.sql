-- Add a public visibility flag to knowledge_base. Defaults to false so
-- every existing and newly created knowledge base is private until an
-- operator explicitly marks it public. The Knowledge Hub portal filters
-- on this column; the CSM Portal ignores it.
ALTER TABLE knowledge_base ADD COLUMN IF NOT EXISTS is_public boolean NOT NULL DEFAULT false;
