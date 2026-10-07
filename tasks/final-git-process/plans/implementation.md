# Final Git process tracking

- Scope: tracked final Integrate for gitops-worker only; preserve final hooks/signing.
- TDD: add controlled post-merge hook and journal/unknown tests; actor execution waits for ROOT token.
- Files: internal/gitops/{delivery.go,candidate_runner.go,*test.go}; internal/gitopsworker/{worker.go,worker_test.go}.
