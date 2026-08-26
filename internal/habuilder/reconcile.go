package habuilder

import (
	"fmt"

	"github.com/jamesmcclay/gopangoblin/internal/habuilder/scm"
)

type reconciler struct {
	client *scm.Client
	mode   Mode
	dryRun bool

	// touched collects the serials of devices whose HA config was actually
	// created, updated, or deleted this run (not ones merely inspected or
	// left alone), so a subsequent push only targets what changed.
	touched []string
}

func (r *reconciler) markTouched(serial string) {
	r.touched = append(r.touched, serial)
}

// role identifies which side of an HA pair a device plays.
type role struct {
	haRole     string // "primary" or "secondary"
	serial     string // this device's serial
	peerSerial string // the other device's serial
	ip         string // this device's control-link (HA1) IP
	peerIP     string // the other device's control-link (HA1) IP
	dataIP     string // this device's data-link (HA2) IP
	peerDataIP string // the other device's data-link (HA2) IP; used as the HA2 gateway on a direct point-to-point link
}

func (r *reconciler) reconcilePair(pair ResolvedFirewallPair, devices []scm.Device) error {
	primary, err := scm.ResolveDeviceBySerial(devices, pair.PrimarySerial)
	if err != nil {
		return fmt.Errorf("primary: %w", err)
	}
	secondary, err := scm.ResolveDeviceBySerial(devices, pair.SecondarySerial)
	if err != nil {
		return fmt.Errorf("secondary: %w", err)
	}

	roles := []struct {
		device scm.Device
		role   role
	}{
		{primary, role{
			haRole: "primary", serial: pair.PrimarySerial, peerSerial: pair.SecondarySerial,
			ip: pair.PrimaryIP, peerIP: pair.SecondaryIP,
			dataIP: pair.PrimaryDataIP, peerDataIP: pair.SecondaryDataIP,
		}},
		{secondary, role{
			haRole: "secondary", serial: pair.SecondarySerial, peerSerial: pair.PrimarySerial,
			ip: pair.SecondaryIP, peerIP: pair.PrimaryIP,
			dataIP: pair.SecondaryDataIP, peerDataIP: pair.PrimaryDataIP,
		}},
	}

	for _, rd := range roles {
		if err := r.reconcileDevice(pair, rd.device, rd.role); err != nil {
			return fmt.Errorf("%s (%s): %w", rd.role.haRole, rd.device.ID, err)
		}
	}
	return nil
}

func (r *reconciler) reconcileDevice(pair ResolvedFirewallPair, device scm.Device, ro role) error {
	if r.mode == ModeUninstall {
		return r.deleteHA(device, ro)
	}

	_, err := r.client.GetHAConfiguration(device.ID)
	if err != nil && !scm.IsNotFound(err) {
		return fmt.Errorf("checking existing HA config: %w", err)
	}
	exists := err == nil

	if exists && r.mode == ModeInstall {
		fmt.Printf("  [skip]   %s %s already has an HA config\n", pair.Name, ro.haRole)
		return nil
	}

	cfg := buildHAConfig(device.ID, pair, ro)

	if exists {
		return r.updateHA(pair, ro, cfg)
	}
	return r.createHA(pair, ro, cfg)
}

func (r *reconciler) createHA(pair ResolvedFirewallPair, ro role, cfg scm.HAConfiguration) error {
	fmt.Printf("  [create] %s %s\n", pair.Name, ro.haRole)
	if r.dryRun {
		return nil
	}
	if _, err := r.client.CreateHAConfiguration(cfg); err != nil {
		return err
	}
	r.markTouched(ro.serial)
	return nil
}

func (r *reconciler) updateHA(pair ResolvedFirewallPair, ro role, cfg scm.HAConfiguration) error {
	fmt.Printf("  [update] %s %s\n", pair.Name, ro.haRole)
	if r.dryRun {
		return nil
	}
	if _, err := r.client.UpdateHAConfiguration(cfg); err != nil {
		return err
	}
	r.markTouched(ro.serial)
	return nil
}

func (r *reconciler) deleteHA(device scm.Device, ro role) error {
	fmt.Printf("  [delete] device %s\n", device.ID)
	if r.dryRun {
		return nil
	}
	err := r.client.DeleteHAConfiguration(device.ID)
	if err != nil {
		if scm.IsNotFound(err) {
			return nil
		}
		return err
	}
	r.markTouched(ro.serial)
	return nil
}

// buildHAConfig builds the SCM HA configuration body for one device in a pair.
func buildHAConfig(deviceID string, pair ResolvedFirewallPair, ro role) scm.HAConfiguration {
	enabled := true
	return scm.HAConfiguration{
		Device:  deviceID,
		Enabled: &enabled,
		Interface: scm.HAInterfaces{
			HA1: scm.HAInterfaceLink{
				Port:      pair.ControlLinkInterface,
				IPAddress: ro.ip,
				Netmask:   pair.Netmask,
				Gateway:   ro.peerIP,
			},
			HA2: scm.HAInterfaceLink{
				Port:      pair.DataLinkInterface,
				IPAddress: ro.dataIP,
				Netmask:   pair.Netmask,
				Gateway:   ro.peerDataIP,
			},
		},
		Group: scm.HAGroup{
			GroupID: pair.GroupID,
			ElectionOption: scm.HAElectionOption{
				HARole: ro.haRole,
			},
			Mode: scm.HAMode{
				ActivePassive: &scm.HAActivePassiveMode{},
			},
			PeerIP:     ro.peerIP,
			PeerSerial: ro.peerSerial,
			StateSynchronization: &scm.HAStateSynchronization{
				Transport: "ethernet",
			},
		},
	}
}
