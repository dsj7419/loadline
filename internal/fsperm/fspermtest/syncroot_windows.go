package fspermtest

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Cloud Files API, through which a cloud service, such as OneDrive,
// registers the folders it keeps in sync.
var (
	cldapi                   = windows.NewLazySystemDLL("cldapi.dll")
	procCfRegisterSyncRoot   = cldapi.NewProc("CfRegisterSyncRoot")
	procCfUnregisterSyncRoot = cldapi.NewProc("CfUnregisterSyncRoot")
)

// syncRegistration is CF_SYNC_REGISTRATION (cfapi.h). Go lays it out as C
// does on every Windows architecture: its GUID is aligned to 4 bytes, and
// nothing else in it to more than a pointer.
type syncRegistration struct {
	StructSize             uint32
	ProviderName           *uint16
	ProviderVersion        *uint16
	SyncRootIdentity       unsafe.Pointer
	SyncRootIdentityLength uint32
	FileIdentity           unsafe.Pointer
	FileIdentityLength     uint32
	ProviderID             windows.GUID
}

// syncPolicies is CF_SYNC_POLICIES (cfapi.h).
type syncPolicies struct {
	StructSize            uint32
	HydrationPrimary      uint16
	HydrationModifier     uint16
	PopulationPrimary     uint16
	PopulationModifier    uint16
	InSync                uint32
	HardLink              uint32
	PlaceholderManagement uint32
}

const (
	// hydrationFull is CF_HYDRATION_POLICY_FULL, and populationAlwaysFull
	// CF_POPULATION_POLICY_ALWAYS_FULL: the service would hand over each
	// file and folder whole, so Windows never asks it for anything.
	hydrationFull        = 2
	populationAlwaysFull = 3
	// syncRootIdentity is what the service keeps of its own on the root.
	syncRootIdentity = "loadline-test"
)

// RegisterSyncRoot registers dir, a folder the test created, as a sync root
// of the cloud service named provider, as such a service registers the
// folder it keeps in sync, and unregisters it when the test ends. No service
// runs: Windows answers for the folder all the same.
//
// It skips the test, naming why, where Windows has no Cloud Files API or
// refuses to register dir, as it does on a file system other than NTFS and
// inside another sync root.
func RegisterSyncRoot(tb testing.TB, dir, provider string) {
	tb.Helper()
	for _, proc := range []*windows.LazyProc{procCfRegisterSyncRoot, procCfUnregisterSyncRoot} {
		if err := proc.Find(); err != nil {
			tb.Skipf("register %s as a sync root: %v", dir, err)
		}
	}
	root := utf16(tb, dir)
	id, err := windows.GenerateGUID()
	if err != nil {
		tb.Fatal(err)
	}
	identity := []byte(syncRootIdentity)
	registration := syncRegistration{
		ProviderName:    utf16(tb, provider),
		ProviderVersion: utf16(tb, "1.0"),
		//nolint:gosec // G103: CfRegisterSyncRoot reads SyncRootIdentityLength bytes there, which identity holds
		SyncRootIdentity:       unsafe.Pointer(&identity[0]),
		SyncRootIdentityLength: uint32(len(syncRootIdentity)),
		ProviderID:             id,
	}
	registration.StructSize = uint32(unsafe.Sizeof(registration))
	policies := syncPolicies{HydrationPrimary: hydrationFull, PopulationPrimary: populationAlwaysFull}
	policies.StructSize = uint32(unsafe.Sizeof(policies))
	//nolint:gosec // G103: CfRegisterSyncRoot reads the path and the two structures it is given, laid out as cfapi.h declares them
	hr, _, _ := procCfRegisterSyncRoot.Call(uintptr(unsafe.Pointer(root)), uintptr(unsafe.Pointer(&registration)),
		uintptr(unsafe.Pointer(&policies)), 0)
	if hr != 0 {
		tb.Skipf("register %s as a sync root: HRESULT 0x%08X", dir, uint32(hr)) //nolint:gosec // G115: an HRESULT is 32 bits
	}
	tb.Logf("registered %s as a sync root of %s", dir, provider)
	tb.Cleanup(func() {
		//nolint:gosec // G103: CfUnregisterSyncRoot reads the path it is given
		if hr, _, _ := procCfUnregisterSyncRoot.Call(uintptr(unsafe.Pointer(root))); hr != 0 {
			tb.Errorf("unregister the sync root %s: HRESULT 0x%08X", dir, uint32(hr)) //nolint:gosec // G115: an HRESULT is 32 bits
		}
	})
}

func utf16(tb testing.TB, s string) *uint16 {
	tb.Helper()
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		tb.Fatal(err)
	}
	return p
}
