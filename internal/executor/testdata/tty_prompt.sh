#!/usr/bin/env bash
# Shows the terminal, then asks with read -p (the prompt only appears on a terminal).
tty
read -r -p "Continue? " answer
echo "answer=$answer"
