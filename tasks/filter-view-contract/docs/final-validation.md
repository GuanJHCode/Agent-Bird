# Filter B/R evidence contract

Production `18fd6b2`, final fixture `e533b70`. New contract types strictly decode bounded canonical/rendered path hashes, validate exact manifest/live-context binding and clone owned slices. The optional filter field is appended to three existing Git receipt/intent DTOs; nil bytes retain the previous encoding. CandidateArtifact carries and hashes the view evidence. No public pipeline or live runtime is created by these DTOs.

ROOT final race: 2 top-level / 5 named results including parents, all PASS, no FAIL/SKIP; related vet exit0. Whole build at unchanged production18fd6b2 exit0. Source/commands/counts/SHA in data/root-verification.json. Independent production/test review passed18fd6b2; final fixture/report review passed065235a.

Actual RED ad2aa1f: all 3 subcases plus parent FAIL. First18fd6b2 race: 3 PASS / 2 FAIL, exit1; the oversized JSON test padded whitespace outside the value, which encoding/json strips before UnmarshalJSON. Fixed fixture places padding inside the object, retaining the actual value-bound assertion; enclosing artifact readers remain responsible for total envelope bytes. First vet also failed on an unkeyed alias literal; it is now keyed. All raw failures are retained. Parent attachment null/duplicate/case handling is still required before the public chain opens.
