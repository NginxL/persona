package sysmon

import (
	"runtime"
	"testing"
)

func TestStorageMountinfoFiltersPseudoAndBindMounts(t *testing.T) {
	raw := []byte(`25 31 0:23 / /sys rw,nosuid,nodev - sysfs sysfs rw
26 31 0:24 / /proc rw,nosuid,nodev - proc proc rw
27 31 0:5 / /dev rw,nosuid - devtmpfs udev rw
29 31 0:26 / /run rw,nosuid,nodev - tmpfs tmpfs rw
30 31 0:28 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw,size=4194304k
31 1 253:2 / / rw,relatime - ext4 /dev/vda2 rw
32 31 253:16 / /data00 rw,relatime - ext4 /dev/vdb rw
33 31 253:16 /sub/logs /var/log/app rw,relatime - ext4 /dev/vdb rw
34 31 7:0 / /snap/core/123 ro,nodev - squashfs /dev/loop0 ro
35 31 0:50 / /var/lib/docker/overlay2/abc/merged rw - overlay overlay rw
36 31 253:1 / /boot/efi ro,relatime - vfat /dev/vda1 ro
`)
	got := storageMountinfo(raw)
	if len(got) != 4 {
		t.Fatalf("got %d mounts (%+v), want 4 (/, /boot/efi, /data00, /tmp)", len(got), got)
	}
	wantMounts := []string{"/", "/boot/efi", "/data00", "/tmp"}
	for i, want := range wantMounts {
		if got[i].Mount != want {
			t.Errorf("mount[%d] = %q, want %q", i, got[i].Mount, want)
		}
	}
	if !got[1].ReadOnly {
		t.Errorf("/boot/efi ReadOnly = false, want true")
	}
	if got[2].Device != "/dev/vdb" || got[2].FSType != "ext4" || got[2].ReadOnly {
		t.Errorf("/data00 = %+v", got[2])
	}
	if got[3].FSType != "tmpfs" {
		t.Errorf("/tmp fstype = %q, want tmpfs", got[3].FSType)
	}
}

func TestStorageMountinfoIncludesContainerOverlayRoot(t *testing.T) {
	raw := []byte(`10 1 0:42 / / rw,relatime - overlay overlay rw
11 10 0:43 / /proc rw - proc proc rw
12 10 0:44 / /dev/shm rw - tmpfs shm rw
13 10 0:45 / /tmp rw - tmpfs tmpfs rw
`)
	got := storageMountinfo(raw)
	if len(got) != 2 || got[0].Mount != "/" || got[0].FSType != "overlay" || got[1].Mount != "/tmp" {
		t.Fatalf("container overlay mounts = %+v", got)
	}
}

func TestStorageMountsFallback(t *testing.T) {
	raw := []byte(`proc /proc proc rw 0 0
/dev/nvme0n1p2 / ext4 rw,relatime 0 0
/dev/nvme0n1p2 /var/bind ext4 rw,relatime 0 0
/dev/nvme1n1 /data00 xfs rw,relatime 0 0
tmpfs /tmp tmpfs rw 0 0
tmpfs /run tmpfs rw 0 0
`)
	got := storageMountsFallback(raw)
	if len(got) != 3 {
		t.Fatalf("got %d mounts (%+v), want 3", len(got), got)
	}
	if got[0].Mount != "/" || got[1].Mount != "/data00" || got[2].Mount != "/tmp" {
		t.Fatalf("unexpected fallback mounts: %+v", got)
	}
}

func TestDarwinIncludeMount(t *testing.T) {
	cases := []struct {
		mount, dev, fs       string
		readOnly, dontBrowse bool
		want                 bool
	}{
		{"/", "/dev/disk3s1s1", "apfs", true, false, true},
		{"/System/Volumes/Data", "/dev/disk3s5", "apfs", false, true, true},
		{"/System/Volumes/VM", "/dev/disk3s6", "apfs", false, true, false},
		{"/System/Volumes/Preboot", "/dev/disk3s2", "apfs", false, true, false},
		{"/Volumes/WorkSSD", "/dev/disk4s1", "apfs", false, false, true},
		{"/dev", "devfs", "devfs", false, true, false},
		{"/dev/fd", "fdesc", "apfs", false, false, false},
		{"/developer", "/dev/disk5s1", "apfs", false, false, true},
		{"/private/var/vm", "/dev/disk3s6", "apfs", false, false, false},
		{"/private/var/vm/swapfile", "/dev/disk3s6", "apfs", false, false, false},
		{"/private/var/vms", "/dev/disk5s2", "apfs", false, false, true},
		{"/System/Volumes/Data/home", "map auto_home", "autofs", false, true, false},
	}
	for _, tc := range cases {
		if got := darwinIncludeMount(tc.mount, tc.dev, tc.fs, tc.readOnly, tc.dontBrowse); got != tc.want {
			t.Errorf("darwinIncludeMount(%q, %q) = %v, want %v", tc.mount, tc.fs, got, tc.want)
		}
	}
}

func TestBuildMountStatCapacityAndInodes(t *testing.T) {
	st := buildMountStat(
		storageMountEntry{Mount: "/data00", Device: "/dev/vdb", FSType: "ext4"},
		1000, 200, 150, 4096,
		10000, 1000,
	)
	if st.UsedBytes != 800*4096 || st.AvailBytes != 150*4096 || st.TotalBytes != 950*4096 {
		t.Fatalf("bytes = %+v", st)
	}
	if st.UsedPercent != 84.2 {
		t.Errorf("UsedPercent = %v, want 84.2", st.UsedPercent)
	}
	if st.InodesTotal != 10000 || st.InodesUsed != 9000 || st.InodesFree != 1000 {
		t.Errorf("inodes = %+v", st)
	}
	if st.InodesPercent == nil || *st.InodesPercent != 90 {
		t.Errorf("InodesPercent = %v, want 90", st.InodesPercent)
	}

	// Filesystem reporting 0 inodes (e.g. FAT/exFAT) leaves InodesPercent nil.
	noInodes := buildMountStat(
		storageMountEntry{Mount: "/boot/efi", Device: "/dev/vda1", FSType: "vfat", ReadOnly: true},
		100, 50, 50, 4096,
		0, 0,
	)
	if noInodes.InodesPercent != nil || !noInodes.ReadOnly {
		t.Errorf("noInodes = %+v", noInodes)
	}
}

func TestStorageMountsLive(t *testing.T) {
	mounts, supported := StorageMounts()
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if !supported {
			t.Fatalf("StorageMounts supported = false on %s", runtime.GOOS)
		}
		if len(mounts) == 0 {
			t.Fatalf("StorageMounts returned 0 mounts on %s", runtime.GOOS)
		}
		for _, m := range mounts {
			if m.Mount == "" || m.TotalBytes <= 0 {
				t.Errorf("invalid mount stat: %+v", m)
			}
		}
	}
}

func BenchmarkStorageMounts(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		mounts, ok := StorageMounts()
		if (runtime.GOOS == "linux" || runtime.GOOS == "darwin") && (!ok || len(mounts) == 0) {
			b.Fatal("expected mounts")
		}
	}
}
