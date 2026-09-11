"""Creation identity comparison shared by the bounded native observers."""


def same_process(current, original):
    # Parent, group and executable can change during this process's lifetime.
    # This observer is bounded; its ps creation timestamp has second precision.
    return current is not None and all(current.get(key) == original.get(key)
                                       for key in ("pid", "started_at_local"))
