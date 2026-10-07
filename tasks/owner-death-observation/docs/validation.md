# Classified owner death observation

ObserveIdentityContext returns Unknown/Alive/Exited/Reused. Only a positive ESRCH from the first kernel probe or the second probe after failed birth observation returns Exited; observed different birth is Reused. Permission failures, failed observations and caller cancellation return Unknown with error. The helper retains the one-second/caller context bound. Existing IdentityExitedContext and its unchanged tests continue to map both Exited and Reused to exact-tuple-gone. All paths use one observation algorithm.

This is not an original Handle/Wait/tree receipt, and cannot retire OwnerPending or prove pre-Bind child cleanup. The new strict successor caller must accept only Exited. Coordinator integration is pending.

The initial missing API is compile RED, not a behavioral defect in the intentional old API. Final macOS arm64 ordinary and race each pass6top33named, zero fail/skip, including three unchanged legacy tests. Injected15-case matrix includes both probes, birth outcomes, input rejection, permission failures and cancellation at each stage. Native owned /bin/sleep is checked alive, then killed and joined via its original Wait before exited observation. An artificial mismatched expected birth is correctly Reused; this is not claimed as actual OS PID reuse.

Linux arm64 same6top33named pass in the pinned offline container, no failure/skip. Exact CID7766782a31815013b20e9e60ab194cd59d05c7d3b24e3496a2d58e24edb296f4 exited0, not OOM, removed and inspected absent. Owned binary removed after receipt SHA256 match. Go1.26.8 vet for process and whole module build exit0. Independent review PASS. Sources, accurate timing/counts, log hashes and cleanup evidence: data/root-receipt.json.

No Provider/model/account, installation, push or release. Full F10/B3 and F01–F13 remain in progress.
