def add(a, b):
    for name, value in (("a", a), ("b", b)):
        if type(value) not in (int, float):
            raise TypeError(
                f"{name} must be int or float, got {type(value).__name__}"
            )
    return a + b
