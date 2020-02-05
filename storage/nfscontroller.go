package storage

import (
	"context"
	"errors"
	"fmt"
	"infinibox-csi-driver/api"
	"path"
	"strconv"

	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/glog"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NFSVolumeServiceType servier type
type NfsVolumeServiceType interface {
	CreateNFSVolume() (*infinidatVolume, error)
	DeleteNFSVolume() error
}

type infinidat struct {
	name              string
	nodeID            string
	version           string
	endpoint          string
	ephemeral         bool
	maxVolumesPerNode int64
}

type infinidatVolume struct {
	VolName       string     `json:"volName"`
	VolID         string     `json:"volID"`
	VolSize       int64      `json:"volSize"`
	VolPath       string     `json:"volPath"`
	IpAddress     string     `json:"ipAddress"`
	VolAccessType accessType `json:"volAccessType"`
	Ephemeral     bool       `json:"ephemeral"`
	ExportID      int64      `json:"exportID"`
	FileSystemID  int64      `json:"fileSystemID"`
	ExportBlock   string     `json:"exportBlock"`
}
type MetaData struct {
	pVName    string
	k8sVer    string
	namespace string
	pvcId     string
	pvcName   string
	pvname    string
}

type accessType int

const (
	dataRoot               = "/fs"
	mountAccess accessType = iota
	blockAccess

	//Infinibox default values
	//Ibox max allowed filesystem
	MaxFileSystemAllowed = 4000
	MountOptions         = "hard,rsize=1024,wsize=1024"
	NfsExportPermissions = "RW"
	NoRootSquash         = true

	// for size conversion
	kib    int64 = 1024
	mib    int64 = kib * 1024
	gib    int64 = mib * 1024
	gib100 int64 = gib * 100
	tib    int64 = gib * 1024
	tib100 int64 = tib * 100
)

func validateParameter(config map[string]string) (bool, map[string]string) {
	compulsaryFields := []string{"pool_name", "nfs_networkspace"}
	validationStatus := true
	validationStatusMap := make(map[string]string)
	for _, param := range compulsaryFields {
		if config[param] == "" {
			validationStatusMap[param] = param + " valume missing"
			validationStatus = false
		}
	}
	log.Debug("parameter Validation completed")
	return validationStatus, validationStatusMap
}

func getPVName(pvName string, pvPrfix string) string {
	if pvPrfix != "" {
		result := strings.HasPrefix(pvName, pvPrfix)
		if !result {
			strArray := strings.Split(pvName, "-")
			if len(strArray) > 1 {
				pvName := pvPrfix + strArray[1]
				return pvName
			}
		}
	}
	return pvName
}

func (nfs *nfsstorage) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	log.Debug("Creating Volume")
	log.Infof("Request parameter %v", req.GetParameters())

	//Adding the the request parameter into Map config
	config := make(map[string]string)
	for key, value := range req.GetParameters() {
		config[key] = value
	}
	pvName := getPVName(req.GetName(), config["vol_prefix"])

	log.Debug("PV name ", pvName)
	validationStatus, validationStatusMap := validateParameter(config)
	if !validationStatus {
		log.Error("Fail to validate the storage class parameter %v ", validationStatusMap)
		return nil, status.Error(codes.InvalidArgument, "Fail to validate the storage class parameter")
	}
	log.Debug("parameter validation success")
	capacity := int64(req.GetCapacityRange().GetRequiredBytes())
	if capacity < gib { //INF90
		capacity = gib
		log.Warn("Volumen Minimum capacity should be greater 1 GB")
	}
	log.Infof("volumen capacity %v", capacity)
	caps := req.GetVolumeCapabilities()
	var accessTypeMount, accessTypeBlock bool
	for _, cap := range caps {
		if cap.GetBlock() != nil {
			accessTypeBlock = true
		}
		if cap.GetMount() != nil {
			accessTypeMount = true
		}
	}
	log.Infoln("accessTypeBlock accessTypeBlock=%v  accessTypeMount=%v", accessTypeBlock, accessTypeMount)
	//nfsVolume := NewCreateNFSVolume(pvName, config, capacity)
	//exportpath := path.Join(dataRoot, pvName)

	nfs.pVName = pvName
	nfs.configmap = config
	nfs.capacity = capacity
	nfs.exportpath = path.Join(dataRoot, pvName)

	infinidatVol, createVolumeErr := nfs.CreateNFSVolume()

	if createVolumeErr != nil {
		log.Errorf("failt to create volume %v", createVolumeErr)
		return &csi.CreateVolumeResponse{}, createVolumeErr
	}

	config["ipAddress"] = (*infinidatVol).IpAddress
	config["volPathd"] = (*infinidatVol).VolPath
	config["volID"] = (*infinidatVol).VolID
	config["volSize"] = strconv.Itoa(int((*infinidatVol).VolSize))
	config["exportID"] = strconv.Itoa(int((*infinidatVol).ExportID))
	config["fileSystemID"] = strconv.Itoa(int((*infinidatVol).FileSystemID))
	config["exportBlock"] = (*infinidatVol).ExportBlock
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      (*infinidatVol).VolID,
			CapacityBytes: capacity,
			VolumeContext: config,
			ContentSource: req.GetVolumeContentSource(),
		},
	}, nil
}

//CreateNFSVolume create volumne method
func (nfs *nfsstorage) CreateNFSVolume() (infinidatVol *infinidatVolume, err error) {

	defer func() {
		if res := recover(); res != nil {
			err = errors.New("error while creating filesystem " + fmt.Sprint(res))
		}
	}()
	log.Debug("CreateNFSVolume")
	validnwlist, err := nfs.cs.api.OneTimeValidation(nfs.configmap["pool_name"], nfs.configmap["nfs_networkspace"])
	if err != nil {
		log.Errorf("fail to validate networkspace : %v", err)
		return nil, err
	}
	nfs.configmap["nfs_networkspace"] = validnwlist
	log.Debug("networkspace validation success")

	err = nfs.createFileSystem()
	if err != nil {
		log.Errorf("fail to create fileSystem %v", err)
		return nil, err
	}

	log.Debug("filesystem created successfully")
	defer func() {
		if res := recover(); res != nil {
			err = errors.New("error while export directory" + fmt.Sprint(res))
		}
		if err != nil && nfs.fileSystemID != 0 {
			glog.Infoln("Seemes to be some problem reverting filesystem:", nfs.fileSystemID)
			nfs.cs.api.DeleteFileSystem(nfs.fileSystemID)
		}
	}()

	err = nfs.createExportPath()
	if err != nil {
		log.Errorf("fail to export path %v", err)
		return nil, err
	}
	log.Debug("exportpath created successfully")

	nfs.ipAddress, err = nfs.cs.getNetworkSpaceIP(nfs.configmap)
	if err != nil {
		log.Errorf("fail to get networkspace ipaddress %v", err)
		return nil, err
	}
	log.Debugf("getNetworkSpaceIP ipAddress", nfs.ipAddress)

	defer func() {
		if res := recover(); res != nil {
			err = errors.New("error while AttachMetadata directory" + fmt.Sprint(res))
		}
		if err != nil && nfs.exportID != 0 {
			glog.Infoln("Seemes to be some problem reverting created export id:", nfs.exportID)
			nfs.cs.api.DeleteExportPath(nfs.exportID)
		}
	}()
	metadata := make(map[string]interface{})
	metadata["host.k8s.pvname"] = nfs.pVName
	metadata["filesystem_type"] = ""
	//attache metadata function need to implement
	_, err = nfs.cs.api.AttachMetadataToObject(nfs.fileSystemID, metadata)
	if err != nil {
		log.Errorf("fail to attache metadata %v", err)
		return nil, err
	}

	log.Debug("metadata attached successfully")
	infinidatVol = &infinidatVolume{
		VolID:        fmt.Sprint(nfs.fileSystemID),
		VolName:      nfs.pVName,
		VolSize:      nfs.capacity,
		VolPath:      nfs.exportpath,
		IpAddress:    nfs.ipAddress,
		ExportID:     nfs.exportID,
		ExportBlock:  nfs.exportBlock,
		FileSystemID: nfs.fileSystemID,
	}
	return
}
func (nfs *nfsstorage) createExportPath() (err error) {
	log.Debug("createExportPath")

	access := nfs.configmap["nfs_export_permissions"]
	if access == "" {
		access = NfsExportPermissions
	}
	rootsquash := nfs.configmap["no_root_squash"]
	if rootsquash == "" {
		rootsquash = fmt.Sprint(NoRootSquash)
	}
	rootsq, _ := strconv.ParseBool(rootsquash)
	var permissionsput []map[string]interface{}

	//client = Ip Address are going update while publishing
	permissionsput = append(permissionsput, map[string]interface{}{"access": access, "no_root_squash": rootsq, "client": "*"})

	var exportFileSystem api.ExportFileSys
	exportFileSystem.FilesystemID = nfs.fileSystemID
	exportFileSystem.Transport_protocols = "TCP"
	exportFileSystem.Privileged_port = true
	exportFileSystem.Export_path = nfs.exportpath
	exportFileSystem.Permissionsput = append(exportFileSystem.Permissionsput, permissionsput...)
	exportResp, err := nfs.cs.api.ExportFileSystem(exportFileSystem)
	if err != nil {
		log.Errorf("fail to export path %v", err)
		return
	}
	nfs.exportID = exportResp.ID
	nfs.exportBlock = exportResp.ExportPath
	log.Debug("export path created success")
	return
}

func (nfs *nfsstorage) createFileSystem() (err error) {
	log.Debug("createFileSystem")
	fileSystemCnt, err := nfs.cs.api.GetFileSystemCount()
	if err != nil {
		log.Errorf("fail to get the filesystem count from Ibox %v", err)
		return
	}
	log.Debugf("Max filesystem allowed on Ibox %v", MaxFileSystemAllowed)
	log.Debugf("Current filesystem count on Ibox %v", fileSystemCnt)

	if fileSystemCnt >= MaxFileSystemAllowed {
		log.Errorf("Ibox not allowed to create new file system")
		err = errors.New("Ibox not allowed to create new file system")
		return
	}
	var namepool = nfs.configmap["pool_name"]
	poolID, err := nfs.cs.api.GetStoragePoolIDByName(namepool)
	if err != nil {
		log.Errorf("fail to get GetPoolID by pool_name %v", namepool)
		return
	}
	var fileSys api.FileSystem
	ssdEnabled := nfs.configmap["ssd_enabled"]
	if ssdEnabled == "" {
		ssdEnabled = fmt.Sprint(false)
	}
	ssd, _ := strconv.ParseBool(ssdEnabled)
	fileSys.PoolID = poolID
	fileSys.Name = nfs.pVName
	fileSys.SsdEnabled = ssd
	fileSys.Provtype = strings.ToUpper(nfs.configmap["provision_type"])
	fileSys.Size = nfs.capacity
	fileSystem, err := nfs.cs.api.CreateFilesystem(fileSys)
	if err != nil {
		log.Errorf("fail to create filesystem %v", err)
		return
	}
	nfs.fileSystemID = fileSystem.ID
	log.Info("filesystem created successfully")
	return
}

func (nfs *nfsstorage) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	log.Debug("DeleteVolume")
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	volumeID := req.GetVolumeId()
	//nfsVolume := NewDeleteNFSVolume(volID)
	volID, err := strconv.ParseInt(volumeID, 10, 64)
	if err != nil {
		log.Errorf("Invalid Volume ID %v", err)
		return &csi.DeleteVolumeResponse{}, nil
	}
	nfs.uniqueID = volID
	nfsDeleteErr := nfs.DeleteNFSVolume()
	if nfsDeleteErr != nil {
		log.Errorf("fail to delete NFS Volume %v", nfsDeleteErr)
		return &csi.DeleteVolumeResponse{}, nil
	}
	log.Infoln("volume %v successfully deleted", volumeID)
	return &csi.DeleteVolumeResponse{}, nil
}

//DeleteNFSVolume delete volumne method
func (nfs *nfsstorage) DeleteNFSVolume() (err error) {
	log.Debug("DeleteNFSVolume")
	defer func() {
		if res := recover(); res != nil {
			err = errors.New("error while deleting filesystem " + fmt.Sprint(res))
		}
	}()

	//1. Delete export path
	exportResp, err := nfs.cs.api.GetExportByFileSystem(nfs.uniqueID)
	if err != nil {
		log.Errorf("failt to delete export path %v", err)
		return
	}
	for _, ep := range *exportResp {
		//err = deleteExport(ep.ID, ep.Block)
		_, err = nfs.cs.api.DeleteExportPath(ep.ID)
		if err != nil {
			log.Errorf("failt to delete export path %v", err)
			return
		}
	}
	log.Debug("Export path deleted successfully")

	//2.delete metadata
	_, err = nfs.cs.api.DetachMetadataFromObject(nfs.uniqueID)
	if err != nil {
		log.Errorf("failt to delete metadata %v", err)
		return
	}
	//*******************************/
	//3. delete file system
	log.Infof("delete FileSystem FileSystemID %v", nfs.uniqueID)
	_, err = nfs.cs.api.DeleteFileSystem(nfs.uniqueID)
	if err != nil {
		log.Errorf("failt to delete filesystem %v", err)
		return
	}
	log.Debug("filesystem deleted successfully")
	return
}

//============================================Unimplemented Methods=========================//

func (nfs *nfsstorage) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return &csi.ControllerPublishVolumeResponse{}, nil
}

func (nfs *nfsstorage) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}
func (nfs *nfsstorage) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	return nil, nil
}

func (nfs *nfsstorage) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, nil
}

func (nfs *nfsstorage) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, nil
}
func (nfs *nfsstorage) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, nil
}
func (nfs *nfsstorage) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return nil, nil
}
func (nfs *nfsstorage) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, nil
}
func (nfs *nfsstorage) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, nil
}

func (nfs *nfsstorage) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
        log.Debug("ExpandVolume")
        if req.GetVolumeId() == "" {
                return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
        }

        if req.GetCapacityRange() == nil {
                return nil, status.Error(codes.InvalidArgument, "CapacityRange cannot be empty")
        }

        volDetails := req.GetVolumeId()
        volDetail := strings.Split(volDetails, "$$")
        ID, err := strconv.ParseInt(volDetail[0], 10, 64)
        if err != nil {
                log.Errorf("Invalid Volume ID %v", err)
                return &csi.ControllerExpandVolumeResponse{}, nil
        }

        capacity := int64(req.GetCapacityRange().GetRequiredBytes())
        if capacity < gib {
                capacity = gib
                log.Warn("Volume Minimum capacity should be greater 1 GB")
        }
        log.Infof("volumen capacity %v", capacity)
        var fileSys api.FileSystem
        fileSys.Size = capacity
        // Expand file system size
        _, err = nfs.cs.api.UpdateFilesystem(ID, fileSys)
        if err != nil {
                log.Errorf("Failed to update file system %v", err)
                return &csi.ControllerExpandVolumeResponse{}, err
        }
        log.Infoln("Filesystem updated successfully")
        return &csi.ControllerExpandVolumeResponse{
                CapacityBytes:         capacity,
                NodeExpansionRequired: false,
        }, nil
}
