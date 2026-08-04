# luna2-client

The Luna node install module: the initramfs code a node boots into, the dracut
`95luna` module and the initramfs-tools hook, plus the `lpart` advanced
partitioner under `advanced-partitioner/`.

## Layout

One committed file tree per distribution family and architecture. `src/` in each
is the buildroot the package is made from.

```
redhat/{x86,arm}/      luna2-client.spec + src/
opensuse/{x86,arm}/    luna2-client.spec + src/
ubuntu/{x86,arm}/      src/DEBIAN/control + src/
advanced-partitioner/  the lpart Go source
VERSION                the only place the version is stated
```

## Packages are built by the pipeline, not by hand

`.gitlab-ci.yml` loads the shared pipeline from
`ClusterVision/gitlab-cicd-pipelines`. One run compiles the `lpart` binaries for
every architecture, **stages them into each package tree**, and produces all six
packages — two rpm families and Ubuntu, x86_64 and aarch64 — on a single x86
runner. Nothing is cross-built on native hardware: the Go side cross-compiles and
the packagers only pack files.

Publishing is decided by the branch. A branch that is not a publishing branch is
still built, so its packages are available as pipeline artifacts.

**The `create_rpm.sh` / `create_deb.sh` scripts remain for local builds** and are
the quickest way to test a packaging change. Be aware that only the two x86 rpm
scripts compile anything; the other four assume the binaries are already staged.

### Version

`VERSION` at the root holds `<version>-<release>`, e.g. `2.2-0`, and is the only
place it is stated. The specs read it through the `luna_version` / `luna_release`
macros and keep the same value as their fallback, so a local `rpmbuild` produces
the same package the pipeline does. Editing that file is how the version moves.

Six trees each carrying their own version is how they drift: before this, the
release numbers had reached 42, 21, 21 and 20 for the same package.

### The lpart binaries in `*/src/usr/bin/`

They are committed **for now**, and they should not be. They exist in git because
four of the six build scripts never compiled anything and could not otherwise
produce a package at all. The pipeline stages them itself, so once a published
release has been confirmed working they come out and the `.gitignore` gains them.

`libluna-fakeuname.so` is a different case and stays: it is a prebuilt artefact
that is committed deliberately and listed in `%files`.

## Notes for anyone touching the packaging

- **Directory modes are not stored by git.** Only file modes are, so directory
  permissions come from whoever's umask did the checkout, and both packagers copy
  them into the package. `dpkg-deb` refuses a control directory of `0777`;
  `rpmbuild` accepts it silently and ships a world-writable `/usr/bin`. The
  pipeline pins them to `0755` before packing. A local build inherits your umask.
- **`BuildArch` is deliberately absent from the specs.** Pinning it to `%{_arch}`
  makes an aarch64 package impossible to build on an x86 machine — `rpmbuild`
  fails with *"No compatible architectures found for build"*. Without it,
  `--target` decides, and a local build still resolves to the host architecture.
- **`%clean` deliberately does nothing.** `rpmbuild` removes `--buildroot` after a
  successful build, and these buildroots are committed trees. Leave it alone.
