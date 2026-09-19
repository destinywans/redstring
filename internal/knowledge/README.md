# Knowledge and confirmed experience

`Service` coordinates three independent contracts: `Repository` for durable source
and generation publication, `Embedder` for model vectors, and `VectorIndex` for
scoped retrieval. The initial `PGRepository` publishes vector chunks and switches
the active generation in one transaction. A future external vector backend must
stage the complete generation before activating it; no distributed transaction
is assumed. Older generation completion cannot replace a newer active generation.

Experience lifecycle:

1. `POST /api/v1/investigations/{id}/knowledge-drafts` (operator) accepts
   `{ "title": "...", "body": "...", "evidence_ids": ["..."] }`.
   It saves an immutable draft with the investigation's application scope and
   1–100 distinct evidence IDs. Every referenced evidence must belong to that
   investigation. Draft creation makes no model or embedding call and creates
   no searchable knowledge document.
2. `GET /api/v1/investigations/{id}/knowledge-drafts` (viewer) returns `{items:[]}`.
   Drafts include their author, creation time and any eventual confirmation link.
3. `POST /api/v1/knowledge-drafts/{id}/confirm` (operator, no body) requires a
   closed investigation. A transaction locks the investigation and draft,
   validates evidence provenance again, creates one immutable source version,
   records the confirming user/time, and appends `knowledge.confirmed`.
   Concurrent confirmation and subsequent retries return the same document.
4. Only the call that creates the source starts indexing after commit. Embedding
   failure leaves the original confirmed document and failed index status intact.
   `POST /api/v1/knowledge/{id}/reindex` retries indexing that same source. A crash
   after confirmation but before indexing can leave `unindexed`; the same reindex
   endpoint recovers it without another approval or duplicate document.

Confirmed documents use `investigation://{id}/knowledge-drafts/{draftID}` as their
source URI and retain `investigation_id`, `evidence_ids`, version 1, body and hash.
There is no update-in-place endpoint; a later draft is a separate immutable source.
Search remains restricted to the requested application plus global sources and
the configured embedding generation. Search citations identify document/source,
version and chunk; fetching the document exposes its investigation provenance.

Draft response fields: `id`, `investigation_id`, `application_id`, `title`, `body`,
`evidence_ids`, `created_by`, `created_at`, plus optional `confirmed_by`,
`confirmed_at`, `document_id`. Confirmation returns the existing `Document` shape
with optional `investigation_id` and `evidence_ids`. Open investigations return
409; invalid evidence or input returns 422; missing resources return 404. Read-only
users cannot create or confirm drafts. The API does not independently decide that
recovery succeeded; the investigation closure workflow owns that decision.

New lifecycle tests are present in `drafts_test.go`, but the latest source is
unverified: test execution was stopped when the user required explicit permission
before further testing. Earlier knowledge test passes do not validate these new
routes or the subsequently corrected test fixture.
