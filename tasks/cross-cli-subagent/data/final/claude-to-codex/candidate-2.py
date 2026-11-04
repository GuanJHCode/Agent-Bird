def add(a, b):
    if type(a) not in (int, float) or type(b) not in (int, float):
        raise TypeError("a and b must each be an int or float")
    return a + b
