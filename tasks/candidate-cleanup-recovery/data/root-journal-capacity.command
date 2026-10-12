TMPDIR=/private/tmp go test -json -count=1 -run ^TestCleanupJournalReservationProtectsRemainingRecords$ ./internal/gitopsworker
