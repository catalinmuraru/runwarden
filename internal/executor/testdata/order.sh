#!/usr/bin/env bash
# Alternates stdout and stderr; the combined output must keep this order.
echo out1; echo err1 >&2; echo out2; echo err2 >&2
