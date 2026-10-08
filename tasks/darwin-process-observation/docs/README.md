# Darwin kernel process observation

The controlled C/POSIX locale path now observes birth, ancestry and process groups directly through Darwin kernel interfaces. Non-C locale and unsupported TZ formats retain the existing ps formatter, preserving persisted identity strings. Group observations do not require locale formatting. Linux paths retain their existing /proc behavior.

The first agent fixture incorrectly used the parent's birth as the child's golden and would append testing output after helper JSON. ROOT corrected it: Start the owned sandbox child, obtain an independent ps golden for that exact child PID, pass it over stdin, and retain one original Wait. The corrected baseline records a real process-fork denial; the fixed version passes with original Wait0. Test output no longer overwrites repository evidence. The initial raw and ROOT fixture failure remain labelled historical, not final acceptance.

Focused ordinary validation: 38 top-level / 103 named PASS, zero failure, one helper-only skip. Includes no-fork sandbox, exact legacy birth format and locale/TZ fallback, actual waitable zombie/group, real parent-child ancestry and identity, cancellation/UID/depth/UNKNOWN guards and original process group lifecycle. Review and final race/public integration evidence are pending. This is not a 60-minute resource result.
