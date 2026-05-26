#!/bin/bash

type getarg >/dev/null 2>&1 || . /lib/dracut-lib.sh

[ $root = "luna" ] && rootok=1
