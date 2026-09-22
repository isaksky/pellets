ALTER TABLE pellets ADD COLUMN model TEXT
    CHECK (model IS NULL OR (length(model) BETWEEN 1 AND 512 AND trim(model) = model));
ALTER TABLE pellets ADD COLUMN reasoning_effort TEXT
    CHECK (reasoning_effort IS NULL OR (length(reasoning_effort) BETWEEN 1 AND 128 AND trim(reasoning_effort) = reasoning_effort));
