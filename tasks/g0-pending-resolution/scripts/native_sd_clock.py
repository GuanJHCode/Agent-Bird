"""The one macOS continuous clock agreed for the new native fixture."""
import time

CLOCK_IMPL = 'clock_gettime_ns(CLOCK_MONOTONIC_RAW)'


def continuous_ns():
    return time.clock_gettime_ns(time.CLOCK_MONOTONIC_RAW)


def clock_pair():
    return {'clock_impl':CLOCK_IMPL,'mono_ns':continuous_ns(),'wall_ns':time.time_ns()}
