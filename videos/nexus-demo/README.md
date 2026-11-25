# Isolated native-film demo

Uses `server.NewServer(store.NewMemStore())`, the real Nexus REST handlers,
permission checks, SHA-256 blob storage, and `core.Call` packaging/restoration.
There are no canned HTTP responses or mocked UI controls. Two synthetic local
identities, Avery (Demo) and Maya (Demo), exist only while this server runs.
This is **not** a production signup, a Postgres deployment test, or evidence
that an external AI provider generated the seeded transcript live.

Listen address is hardcoded to loopback. Signing keys are random on each boot;
tokens expire in four hours and stay in a dedicated ignored state directory.
Never use production configuration or copy private source into the demo folder.

Build: `go build -o videos/nexus-demo/runtime/nexus-demo.exe ./videos/nexus-demo`
Start with `--state-dir <dedicated-runtime> --workspace <this-folder>/workspace`.
The normal native app can then use the generated sender/recipient config via
`CENTRAL_MEMORY_CONFIG_DIR`, without modifying the real sign-in.

The `--action send|accept|restore` commands exercise actual transfers through
the real handlers. Restore writes only a new directory and compares every
restored file's hash with the synthetic source. No command executes a harness.
