# Investigation workspace

`InvestigationPage` owns investigation canvas, evidence comparison, merge review and per-user layout. `preview.ts` is an explicit, isolated design preview; these fixtures never enter API query results or write requests. `?preview=1&view=canvas|evidence|merge` is interpreted by the enclosing workspace.

- Canonical investigation/branch/messages/evidence/run data come from REST. `assistant-ui` ExternalStoreRuntime renders the selected branch's backend-owned messages; the inline card and sidebar share the same draft.
- SSE `update` events deduplicate on durable `seq`; browser EventSource resumes with Last-Event-ID. Attempt deltas are transient display data, keyed by attempt; only committed history is used by subsequent model calls.
- Messages retain a client request ID after request failure, so retrying the same text does not duplicate a committed run. Different text creates a new request.
- Branches inherit an explicit committed revision. Merge previews expose source/target versions, message selection, evidence-only mode and conflict acknowledgement; 409 invalidates the old preview.
- Layout and drafts use the existing user-specific layout endpoint. Debounced saves are serialized to prevent older responses overwriting newer content. Route exit flushes the latest draft; unsaved full-page navigation is guarded. Layout errors remain visible.
- Canvas cards preserve dragged positions. Hiding cards never cancels backend work. Branch and evidence cards support expanded, collapsed, icon, hidden, viewport-pinned and edge-docked presentation. Real evidence nodes show source text and remain separately draggable. Closing the chat sidebar leaves separately docked cards accessible.
- Knowledge citations are inserted into the selected or explicitly requested branch draft, never submitted automatically.

The eight approved source images are the visual direction. This module covers images 02, 03 and 04. Preview curves and logs are labeled sample data. Live evidence displays actual submitted content rather than manufacturing dashboard values. Production chart parsing requires a typed telemetry renderer; arbitrary evidence text is not converted into invented series. New/fork forms use the shared Radix Modal for focus trapping, Escape and focus restoration.

Validation: scoped TypeScript compilation only by this worker. Parent owns browser visual/interaction verification and final integration. No cluster, real model or broad backend tests were run.
