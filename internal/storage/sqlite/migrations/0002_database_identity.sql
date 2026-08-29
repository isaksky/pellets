INSERT INTO application_metadata(key, value)
VALUES
    (
        'database_id',
        lower(
            hex(randomblob(4)) || '-' ||
            hex(randomblob(2)) || '-4' ||
            substr(hex(randomblob(2)), 2) || '-' ||
            substr('89ab', 1 + abs(random() % 4), 1) ||
            substr(hex(randomblob(2)), 2) || '-' ||
            hex(randomblob(6))
        )
    ),
    ('created_at_julian', CAST(julianday('now') AS TEXT)),
    ('product', 'pellets')
ON CONFLICT(key) DO NOTHING;
