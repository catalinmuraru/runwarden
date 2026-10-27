#!/bin/bash
# Prints the terminal size, then again after one line of input (sent after a resize).
stty size
read -r _
stty size
