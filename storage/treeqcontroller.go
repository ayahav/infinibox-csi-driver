/*Copyright 2020 Infinidat
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.*/
package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	log "infinibox-csi-driver/helper/logger"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (treeq *treeqstorage) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (csiResp *csi.CreateVolumeResponse, err error) {
	var treeqVolumeMap map[string]string
	config := req.GetParameters()
	pvName := req.GetName()
	log.Debugf("Creating fileystem %s of nfs_treeq protocol ", pvName)

	//Validating the reqired parameters
	validationStatus, validationStatusMap := treeq.filesysService.validateTreeqParameters(config)
	if !validationStatus {
		log.Errorf("Fail to validate parameter for nfs_treeq protocol %v ", validationStatusMap)
		return nil, status.Error(codes.InvalidArgument, "Fail to validate parameter for nfs_treeq protocol")
	}

	capacity := int64(req.GetCapacityRange().GetRequiredBytes())
	if capacity < gib {
		capacity = gib
		log.Warn("Volume Minimum capacity should be greater 1 GB")
	}
	treeqVolumeMap, err = treeq.filesysService.IsTreeqAlreadyExist(config["pool_name"], strings.Trim(config["network_space"], ""), pvName)
	if len(treeqVolumeMap) == 0 && err == nil {
		treeqVolumeMap, err = treeq.filesysService.CreateTreeqVolume(config, capacity, pvName)
	} 
	if err != nil {
		log.Errorf("fail to create volume %v", err)
		return &csi.CreateVolumeResponse{}, err
	}
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      treeqVolumeMap["ID"] + "#" + treeqVolumeMap["TREEQID"] + "#" + config[MAXFILESYSTEMSIZE],
			CapacityBytes: capacity,
			VolumeContext: treeqVolumeMap,
			ContentSource: req.GetVolumeContentSource(),
		},
	}, nil
}

func getVolumeIDs(volumeID string) (filesystemID, treeqID int64, size string, err error) {
	volproto := strings.Split(volumeID, "#")
	if len(volproto) != 3 {
		err = errors.New("volume Id and other details not found")
		return
	}
	filesystemID, err = strconv.ParseInt(volproto[0], 10, 64)
	treeqID, err = strconv.ParseInt(volproto[1], 10, 64)
	size = volproto[2]
	return
}

func (treeq *treeqstorage) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	filesystemID, treeqID, _, err := getVolumeIDs(req.GetVolumeId())
	if err != nil {
		log.Errorf("Invalid Volume ID %v", err)
		return nil, status.Error(codes.InvalidArgument, "Invalid volume ID")
	}
	nfsDeleteErr := treeq.filesysService.DeleteTreeqVolume(filesystemID, treeqID)
	if nfsDeleteErr != nil {
		if strings.Contains(nfsDeleteErr.Error(), "FILESYSTEM_NOT_FOUND") {
			log.Error("treeq already delete from infinibox")
			return &csi.DeleteVolumeResponse{}, nil
		}
		return &csi.DeleteVolumeResponse{}, nfsDeleteErr
	}
	log.Infof("treeq ID %s successfully deleted", req.GetVolumeId())
	return &csi.DeleteVolumeResponse{}, nil
}

func (treeq *treeqstorage) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return &csi.ControllerPublishVolumeResponse{}, nil

}

func (treeq *treeqstorage) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (treeq *treeqstorage) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return &csi.CreateSnapshotResponse{}, status.Error(codes.Unimplemented, "Unsupported operation for treeq")
}

func (treeq *treeqstorage) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return &csi.DeleteSnapshotResponse{}, status.Error(codes.Unimplemented, "Unsupported operation for treeq")

}

func (treeq *treeqstorage) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (expandVolume *csi.ControllerExpandVolumeResponse, err error) {
	defer func() {
		if res := recover(); res != nil && err == nil {
			err = errors.New("Recovered from CSI ControllerExpandVolume " + fmt.Sprint(res))
		}
	}()

	filesystemID, treeqID, maxSize, err := getVolumeIDs(req.GetVolumeId())
	if err != nil {
		log.Errorf("Invalid Volume ID %v", err)
		return nil, status.Error(codes.InvalidArgument, "Invalid volume ID")
	}

	capacity := int64(req.GetCapacityRange().GetRequiredBytes())
	if capacity < gib {
		capacity = gib
		log.Warn("Volume Minimum capacity should be greater 1 GB")
	}

	err = treeq.filesysService.UpdateTreeqVolume(filesystemID, treeqID, capacity, maxSize)
	if err != nil {
		return
	}
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         capacity,
		NodeExpansionRequired: false,
	}, nil
}
