-- Synthetic library used by `klaras dev-seed` and the query-plan tests.
-- Deterministic: setseed() is fixed so query plans are comparable across runs.
-- {{N_BOOKS}} is substituted by internal/devseed before execution.
SELECT setseed(0.42);

TRUNCATE books, authors, series, publishers, tags,
         book_authors, book_tags, identifiers, book_files RESTART IDENTITY CASCADE;

-- Bulk load with the denormalisation triggers off; one rebuild at the end is
-- far cheaper than firing them per statement. This mirrors what the real
-- importer does.
ALTER TABLE book_authors DISABLE TRIGGER USER;
ALTER TABLE book_tags    DISABLE TRIGGER USER;

INSERT INTO authors (name, sort)
SELECT n.name, author_sort_of(n.name)
FROM (
    SELECT DISTINCT ON (nm) nm AS name
    FROM (
        SELECT (ARRAY['Anna','Erik','Stieg','Karin','Lars','Maj','Henning','Camilla','Johan','Astrid',
                      'David','Sara','Michael','Elena','Tomas','Nina','Peter','Klara','Oscar','Maria'])
               [1 + (i % 20)] || ' ' ||
               (ARRAY['Larsson','Lindqvist','Mankell','Persson','Nesbo','Fossum','Adler','Holt','Ekman',
                      'Bergman','Sundstrom','Alvtegen','Marklund','Theorin','Kepler','Ahnhem'])
               [1 + ((i / 20) % 16)] || ' ' || (i / 320)::text AS nm
        FROM generate_series(0, 2999) i
    ) s
) n;

INSERT INTO series (name, sort)
SELECT 'Series ' || i, 'Series ' || i FROM generate_series(1, 800) i;

INSERT INTO publishers (name)
SELECT 'Publisher ' || i FROM generate_series(1, 120) i;

INSERT INTO tags (name)
SELECT unnest(ARRAY['Crime','Thriller','Fantasy','Science Fiction','Romance','History','Biography',
                    'Poetry','Horror','Mystery','Swedish','English','Classic','Contemporary','Young Adult',
                    'Non-fiction','Essays','Travel','Philosophy','Politics','Nature','Cooking','Art',
                    'Music','Technology','Science','Medicine','Economics','Humour','Drama']);

-- Books.
INSERT INTO books (uuid, calibre_id, title, description, series_id, series_index,
                   publisher_id, pubdate, rating, languages, path, has_cover, added_at, updated_at)
SELECT
    gen_random_uuid(),
    i,
    (ARRAY['The','A','En','Ett','Den',''])[1 + (i % 6)] ||
        CASE WHEN (i % 6) = 5 THEN '' ELSE ' ' END ||
        (ARRAY['Shadow','River','Winter','Silent','Crimson','Hollow','Northern','Broken','Glass',
               'Iron','Salt','Ember','Quiet','Distant','Golden','Pale','Storm','Rose','Bone',
               'Amber','Frozen','Hidden','Röda','Svarta','Ödets'])[1 + (i % 25)] || ' ' ||
        (ARRAY['House','Garden','Machine','Country','Hour','Season','Bridge','Harbour','Rummet',
               'Kingdom','Archive','Question','Winter','Signal','Orchard','Passage'])[1 + ((i / 25) % 16)],
    'A synthetic description for book ' || i || '. ' ||
        repeat('Lorem ipsum dolor sit amet, consectetur adipiscing elit. ', 3),
    CASE WHEN i % 3 = 0 THEN 1 + (i % 800) ELSE NULL END,
    CASE WHEN i % 3 = 0 THEN ((i % 12) + 1)::numeric ELSE NULL END,
    1 + (i % 120),
    DATE '1950-01-01' + ((i * 7) % 27000),
    CASE WHEN i % 4 = 0 THEN (i % 11) ELSE NULL END,
    CASE WHEN i % 5 = 0 THEN ARRAY['swe'] ELSE ARRAY['eng'] END,
    'synthetic/' || (i / 100)::text || '/' || i::text,
    true,
    now() - ((i % 3000) || ' days')::interval,
    now() - ((i % 3000) || ' days')::interval
FROM generate_series(1, {{N_BOOKS}}) i;

-- 1-3 authors per book.
INSERT INTO book_authors (book_id, author_id, position)
SELECT b.id, 1 + ((b.id * 7 + p) % 3000), p
FROM books b
CROSS JOIN LATERAL generate_series(0, (b.id % 3)) p
ON CONFLICT DO NOTHING;

-- 2-4 tags per book.
INSERT INTO book_tags (book_id, tag_id)
SELECT b.id, 1 + ((b.id * 13 + p) % 30)
FROM books b
CROSS JOIN LATERAL generate_series(0, 1 + (b.id % 3)) p
ON CONFLICT DO NOTHING;

INSERT INTO book_files (book_id, format, filename, size_bytes, sha256)
SELECT b.id, 'EPUB', 'book.epub', 400000 + (b.id % 900000), sha256(b.id::text::bytea)
FROM books b;

INSERT INTO identifiers (book_id, scheme, value)
SELECT b.id, 'isbn', '978' || lpad(b.id::text, 10, '0') FROM books b;

ALTER TABLE book_authors ENABLE TRIGGER USER;
ALTER TABLE book_tags    ENABLE TRIGGER USER;

SELECT refresh_all_book_denorm() AS rows_refreshed;

ANALYZE;
