package gitops

import (
	"context"
	"fmt"
)

// Recheck source integration's exact target under the existing common lock.
// This is called only from Host candidate operations, never Store admission.
func ValidateNewBaseTarget(ctx context.Context, repo, target, ref, head, expectedCommon string, expectedIdentity DirectoryIdentity) error {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	common, err := commonDir(ctx, repo)
	if err != nil {
		return err
	}
	if common != expectedCommon {
		return fail(CodeIdentityChanged, "new base repository", fmt.Errorf("common directory changed"))
	}
	lock, err := acquireCandidateLock(ctx, common)
	if err != nil {
		return err
	}
	defer releaseIntegrationLock(lock)
	b, err := TargetReviewBinding(ctx, repo, target, ref, head, head, nil, 1)
	if err != nil {
		return err
	}
	if !sameDirectoryIdentity(b.TargetDirectoryIdentity, expectedIdentity) {
		return fail(CodeIdentityChanged, "new base target", fmt.Errorf("directory identity changed"))
	}
	return nil
}
