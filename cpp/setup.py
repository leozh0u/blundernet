"""Build the C++ MCTS core:  pip install pybind11 && python cpp/setup.py build_ext --inplace"""
from pybind11.setup_helpers import Pybind11Extension, build_ext
from setuptools import setup

setup(
    name="blundercore",
    version="0.1",
    ext_modules=[Pybind11Extension("blundercore", ["cpp/mctscore.cpp"],
                                   extra_compile_args=["-O3"])],
    cmdclass={"build_ext": build_ext},
)
