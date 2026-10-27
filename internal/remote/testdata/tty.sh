#!/bin/bash
# Prints the terminal size, then again after one line of input.
tty
stty size
read -r _
stty size
