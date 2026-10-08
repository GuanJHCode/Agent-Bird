# Darwin process observation regression

The native helper runs under sandbox-exec with process-fork denied. The parent
writes raw JSON to data and retains the original command Wait. A ps-backed
implementation is expected to fail this test.
