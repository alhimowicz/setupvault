%global debug_package %{nil}

Name:           setupvault
Version:        0.3.0
Release:        3%{?dist}
Summary:        Encrypted Fedora and GNOME setup backups in a terminal
License:        MIT AND BSD-3-Clause
URL:            https://github.com/alhimowicz/setupvault
Source0:        setupvault-%{version}.tar.gz
BuildRequires:  golang >= 1.26
BuildRequires:  systemd-rpm-macros
BuildRequires:  restic >= 0.17
BuildRequires:  rclone
Requires:       restic >= 0.17
Requires:       rclone
Requires:       libsecret
Requires:       polkit
Requires:       policycoreutils
Requires:       systemd
Requires:       dconf
Requires:       flatpak
Requires:       dnf5
Requires:       tar
Requires:       dbus-daemon
Requires:       procps-ng
Requires:       glib2
Requires:       gnupg2

%description
SetupVault is a Bubble Tea terminal interface for encrypted, versioned backups
of app settings, package inventories, GNOME preferences and selected system
configuration. It uses restic for backup encryption and rclone for cloud access.

%prep
%autosetup

%build
export CGO_ENABLED=0
export GOTOOLCHAIN=local
export GOPROXY=off
export GOSUMDB=off
go build -mod=vendor -trimpath -buildvcs=false -ldflags '-s -w -X main.version=%{version}' -o setupvault ./cmd/setupvault

%install
install -Dpm0755 setupvault %{buildroot}%{_bindir}/setupvault
install -Dpm0644 packaging/setupvault.desktop %{buildroot}%{_datadir}/applications/setupvault.desktop
install -Dpm0644 packaging/setupvault-system-export@.service %{buildroot}%{_unitdir}/setupvault-system-export@.service
install -Dpm0644 packaging/setupvault.tmpfiles %{buildroot}%{_tmpfilesdir}/setupvault.conf
mkdir -p %{buildroot}%{_licensedir}/%{name}/third-party
cd vendor
find . -type f -name 'LICENSE*' -exec install -Dpm0644 '{}' %{buildroot}%{_licensedir}/%{name}/third-party/'{}' \;

%check
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
go test -mod=vendor ./...

%post
%tmpfiles_create %{_tmpfilesdir}/setupvault.conf
%systemd_post setupvault-system-export@.service

%preun
%systemd_preun setupvault-system-export@.service

%postun
%systemd_postun setupvault-system-export@.service
if [ "$1" -eq 0 ]; then
    rm -f /etc/polkit-1/rules.d/90-setupvault-[0-9]*.rules
fi

%files
%license LICENSE
%license %{_licensedir}/%{name}/third-party
%doc README.md
%{_bindir}/setupvault
%{_datadir}/applications/setupvault.desktop
%{_unitdir}/setupvault-system-export@.service
%{_tmpfilesdir}/setupvault.conf

%changelog
* Sat Oct 24 2026 Denis Alhimovich <alhimowicz@gmail.com> - 0.3.0-1
- Add reviewed asynchronous app and settings recovery with resume and settings undo
- Restore terminal and shell preferences and report missing cloud dependencies
- Install system Flatpaks with an isolated D-Bus session

* Sat Oct 24 2026 Denis Alhimovich <alhimowicz@gmail.com> - 0.2.2-2
- Keep terminal help visible and scroll long content on small screens

* Sat Oct 24 2026 Denis Alhimovich <alhimowicz@gmail.com> - 0.2.2-1
- Cancel restic gracefully and recover stale locks without removing active locks
- Wait for repository cleanup when exiting the terminal interface
- Correct verification status and display saved failure reasons

* Sat Oct 24 2026 Denis Alhimovich <alhimowicz@gmail.com> - 0.2.1-1
- Animate asynchronous actions and wrap long notices to the terminal width
- Restore editor keyboard modes and allow removing custom backup paths
- Split terminal interface code by responsibility

* Sat Oct 24 2026 Denis Alhimovich <alhimowicz@gmail.com> - 0.2.0-1
- Add command flags, structured JSON responses and unattended recovery
- Detect background terminal launches before starting the interface

* Sat Oct 24 2026 Denis Alhimovich <alhimowicz@gmail.com> - 0.1.1-1
- Exclude VS Code extension downloads and compiled caches from setup backups

* Sat Oct 24 2026 Denis Alhimovich <alhimowicz@gmail.com> - 0.1.0-1
- Initial terminal interface, encrypted backup and safe staged restore
