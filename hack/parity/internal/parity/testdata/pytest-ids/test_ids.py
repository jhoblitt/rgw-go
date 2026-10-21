import pytest


def test_plain():
    pass


@pytest.mark.parametrize("p", ["a/b", "c::d", "e.py", "f[g]", "x-y"])
def test_param(p):
    pass


@pytest.mark.parametrize("a,b", [(1, "s3"), (2, "v4")])
def test_pairs(a, b):
    pass


class TestGroup:
    def test_method(self):
        pass

    @pytest.mark.parametrize("q", ["one", "two::three"])
    def test_method_param(self, q):
        pass
