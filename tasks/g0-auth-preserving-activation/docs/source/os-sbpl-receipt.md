# macOS SBPL receipt

The host profiles were read-only references for the direct Unix path grammar. No profile was modified.

| path | SHA-256 | line used |
| --- | --- | --- |
| `/System/Library/Sandbox/Profiles/com.apple.auearlyboot.sb` | `340904f7fb6207144290c00824bdb57c6ae97a034b16e901b5269a60dc235088` | 68: `(allow network-outbound (literal "/private/var/run/mDNSResponder"))` |
| `/System/Library/Sandbox/Profiles/com.apple.CryptoTokenKit.ctkahp.sb` | `df0a455a30610ac58f3b1b0eee1b83aee32bc38b619f682cb09cd283a6ce1cf9` | 124: `(allow network-outbound (subpath "/private/var/run"))` |

The guard uses direct `literal` rules for its private/public task sockets and a direct `literal` bind rule for the backend. The test additionally probes a non-credential `bootstrap_look_up` for `com.apple.securityd` under the generated profile; the sandboxed return differs from the unsandboxed `BOOTSTRAP_UNKNOWN_SERVICE` baseline, while the profile contains the explicit `mach-lookup` deny.
