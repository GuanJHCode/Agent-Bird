TMPDIR=/private/tmp go test -json -race -count=1 -run TestCleanupRecovery|TestCleanupHistory|TestCleanupJournalRejects ./internal/gitopsworker
