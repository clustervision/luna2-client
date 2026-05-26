#!/bin/bash

rpmbuild --buildroot `pwd`/src -ba luna2-client.spec
