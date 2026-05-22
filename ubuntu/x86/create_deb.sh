#!/bin/sh

if [ $UID != 0 ] && [ -z "`which fakeroot`" ]; then
    echo Please install fakeroot first, or run this script as root!
    exit 1
fi

if [ -z "`which dpkg`" ]; then
    echo Please install dpkg first!
    exit 1
fi

fakeroot dpkg-deb --build src luna2-client.deb

