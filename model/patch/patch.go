package patch

import (
	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/thirdparty"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"io/ioutil"
	"time"
)

// Hard limit on patch size.
const SizeLimit = 1024 * 1024 * 100

// Stores all details related to a patch request
type Patch struct {
	Id            bson.ObjectId `bson:"_id,omitempty"`
	Description   string        `bson:"desc"`
	Project       string        `bson:"branch"`
	Githash       string        `bson:"githash"`
	PatchNumber   int           `bson:"patch_number"`
	Author        string        `bson:"author"`
	Version       string        `bson:"version"`
	Status        string        `bson:"status"`
	CreateTime    time.Time     `bson:"create_time"`
	StartTime     time.Time     `bson:"start_time"`
	FinishTime    time.Time     `bson:"finish_time"`
	BuildVariants []string      `bson:"build_variants"`
	Tasks         []string      `bson:"tasks"`
	Patches       []ModulePatch `bson:"patches"`
	Activated     bool          `bson:"activated"`
	PatchedConfig string        `bson:"patched_config"`
}

// this stores request details for a patch
type ModulePatch struct {
	ModuleName string   `bson:"name"`
	Githash    string   `bson:"githash"`
	PatchSet   PatchSet `bson:"patch_set"`
}

// this stores information about the actual patch
type PatchSet struct {
	Patch       string               `bson:"patch,omitempty"`
	PatchFileId string               `bson:"patch_file_id,omitempty"`
	Summary     []thirdparty.Summary `bson:"summary"`
}

func (p *Patch) SetDescription(desc string) error {
	p.Description = desc
	return UpdateOne(
		bson.M{IdKey: p.Id},
		bson.M{
			"$set": bson.M{
				DescriptionKey: desc,
			},
		},
	)
}

// ClearPatchData removes any inline patch data stored in this patch object for patches that have
// an associated id in gridfs, so that it can be stored properly.
func (p *Patch) ClearPatchData() {
	for i, patchPart := range p.Patches {
		// If the patch isn't stored externally, no need to do anything.
		if patchPart.PatchSet.PatchFileId != "" {
			p.Patches[i].PatchSet.Patch = ""
		}
	}
}

// FetchPatchFiles dereferences externally-stored patch diffs by fetching them from gridfs
// and placing their contents into the patch object.
func (p *Patch) FetchPatchFiles() error {
	for i, patchPart := range p.Patches {
		// If the patch isn't stored externally, no need to do anything.
		if patchPart.PatchSet.PatchFileId == "" {
			continue
		}

		file, err := db.GetGridFile(GridFSPrefix, patchPart.PatchSet.PatchFileId)
		if err != nil {
			return err
		}
		defer file.Close()
		raw, err := ioutil.ReadAll(file)
		if err != nil {
			return err
		}
		p.Patches[i].PatchSet.Patch = string(raw)
	}
	return nil
}

func (p *Patch) SetVariantsAndTasks(variants []string,
	tasks []string) error {
	p.Tasks = tasks
	p.BuildVariants = variants
	return UpdateOne(
		bson.M{
			IdKey: p.Id,
		},
		bson.M{
			"$set": bson.M{
				BuildVariantsKey: variants,
				TasksKey:         tasks,
			},
		},
	)
}

// AddBuildVariants adds more buildvarints to a patch document.
// This is meant to be used after initial patch creation.
func (p *Patch) AddBuildVariants(bvs []string) error {
	change := mgo.Change{
		Update: bson.M{
			"$addToSet": bson.M{BuildVariantsKey: bson.M{"$each": bvs}},
		},
		ReturnNew: true,
	}
	_, err := db.FindAndModify(Collection, bson.M{IdKey: p.Id}, nil, change, p)
	return err
}

// AddTasks adds more tasks to a patch document.
// This is meant to be used after initial patch creation, to reconfigure the patch.
func (p *Patch) AddTasks(tasks []string) error {
	change := mgo.Change{
		Update: bson.M{
			"$addToSet": bson.M{TasksKey: bson.M{"$each": tasks}},
		},
		ReturnNew: true,
	}
	_, err := db.FindAndModify(Collection, bson.M{IdKey: p.Id}, nil, change, p)
	return err
}

// TryMarkStarted attempts to mark a patch as started if it
// isn't already marked as such
func TryMarkStarted(versionId string, startTime time.Time) error {
	filter := bson.M{
		VersionKey: versionId,
		StatusKey:  evergreen.PatchCreated,
	}
	update := bson.M{
		"$set": bson.M{
			StartTimeKey: startTime,
			StatusKey:    evergreen.PatchStarted,
		},
	}
	err := UpdateOne(filter, update)
	if err == mgo.ErrNotFound {
		return nil
	}
	return err
}

// TryMarkFinished attempts to mark a patch of a given version as finished.
func TryMarkFinished(versionId string, finishTime time.Time, status string) error {
	filter := bson.M{VersionKey: versionId}
	update := bson.M{
		"$set": bson.M{
			FinishTimeKey: finishTime,
			StatusKey:     status,
		},
	}
	return UpdateOne(filter, update)
}

// Insert inserts the patch into the db, returning any errors that occur
func (p *Patch) Insert() error {
	return db.Insert(Collection, p)
}

// ConfigChanged looks through the parts of the patch and returns true if the
// passed in remotePath is in the the name of the changed files that are part
// of the patch
func (p *Patch) ConfigChanged(remotePath string) bool {
	for _, patchPart := range p.Patches {
		if patchPart.ModuleName == "" {
			for _, summary := range patchPart.PatchSet.Summary {
				if summary.Name == remotePath {
					return true
				}
			}
			return false
		}
	}
	return false
}

// SetActivated sets the patch to activated in the db
func (p *Patch) SetActivated(versionId string) error {
	p.Version = versionId
	p.Activated = true
	return UpdateOne(
		bson.M{IdKey: p.Id},
		bson.M{
			"$set": bson.M{
				ActivatedKey: true,
				VersionKey:   versionId,
			},
		},
	)

}

// Add or update a module within a patch.
func (p *Patch) UpdateModulePatch(modulePatch ModulePatch) error {
	// check that a patch for this module exists
	query := bson.M{
		IdKey: p.Id,
		PatchesKey + "." + ModulePatchNameKey: modulePatch.ModuleName,
	}
	update := bson.M{
		"$set": bson.M{PatchesKey + ".$": modulePatch},
	}
	result, err := UpdateAll(query, update)
	if err != nil {
		return err
	}

	// The patch already existed in the array, and it's been updated.
	if result.Updated > 0 {
		return nil
	}

	//it wasn't in the array, we need to add it.
	query = bson.M{IdKey: p.Id}
	update = bson.M{
		"$push": bson.M{PatchesKey: modulePatch},
	}
	return UpdateOne(query, update)
}

// RemoveModulePatch removes a module that's part of a patch request
func (p *Patch) RemoveModulePatch(moduleName string) error {
	// check that a patch for this module exists
	query := bson.M{
		IdKey: p.Id,
	}
	update := bson.M{
		"$pull": bson.M{
			PatchesKey: bson.M{ModulePatchNameKey: moduleName},
		},
	}
	return UpdateOne(query, update)
}
