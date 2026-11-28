-- DEV ONLY sample data. Run in pgAdmin Query Tool after migrations/000001_init.up.sql (or just run the API once).
-- The password_hash and encryption values are dummies: you cannot log in as this user.
-- Create real users through POST /api/v1/auth/register once the API exists.

WITH u AS (
    INSERT INTO users (email, password_hash, terms_accepted_at)
    VALUES ('demo@example.com', 'DUMMY-NOT-A-REAL-ARGON2ID-HASH', now())
    RETURNING id
),
d AS (
    INSERT INTO documents (owner_id, name, content_type, size_bytes, storage_url,
                           wrapped_dek, kek_id, nonce)
    SELECT id, 'sample-contract.pdf', 'application/pdf', 123456,
           'dev/sample-contract.pdf.enc',
           decode('00112233445566778899aabbccddeeff', 'hex'),
           'dev-kek/v1',
           decode('aabbccddeeff001122334455', 'hex')
    FROM u
    RETURNING id, owner_id
),
s AS (
    INSERT INTO document_shares (document_id, recipient_email, token_hash,
                                 expires_at, max_opens)
    SELECT id, 'recipient@example.com', 'dev-token-hash-1',
           now() + interval '7 days', 3
    FROM d
    RETURNING id, document_id
)
INSERT INTO audit_logs (user_id, document_id, share_id, action, ip_address)
SELECT d.owner_id, d.id, s.id, 'share.create', '127.0.0.1'
FROM d JOIN s ON s.document_id = d.id;

-- Check the result:
SELECT 'users' AS tbl, count(*) FROM users
UNION ALL SELECT 'documents', count(*) FROM documents
UNION ALL SELECT 'document_shares', count(*) FROM document_shares
UNION ALL SELECT 'audit_logs', count(*) FROM audit_logs;