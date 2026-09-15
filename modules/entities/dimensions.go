package entities

import dataentities "github.com/go-mclib/data/entities"

// mobDimensions applies synchronized age/size/pose metadata before the scale
// attribute. Values are the current vanilla getDefaultDimensions overrides.
func mobDimensions(e *Entity, pose int32, width, height float32) (float32, float32) {
	switch e.TypeName {
	case "minecraft:slime", "minecraft:magma_cube", "minecraft:sulfur_cube":
		size := float32(max(1, min(metadataInt(e, dataentities.AbstractCubeMobIndexSize), 127)))
		return width * size, height * size
	case "minecraft:phantom":
		size := max(0, min(metadataInt(e, dataentities.PhantomIndexSize), 64))
		scale := float32(1) + float32(0.15)*float32(size)
		return width * scale, height * scale
	case "minecraft:pufferfish":
		scale := float32(1)
		switch metadataInt(e, dataentities.PufferfishIndexPuffState) {
		case 0:
			scale = 0.5
		case 1:
			scale = 0.7
		}
		return width * scale, height * scale
	case "minecraft:salmon":
		scale := float32(1)
		if e.Metadata.Get(dataentities.SalmonIndexVariant) != nil {
			switch metadataInt(e, dataentities.SalmonIndexVariant) {
			case 0:
				scale = 0.5
			case 2:
				scale = 1.5
			}
		}
		return width * scale, height * scale
	}
	babyIndex := byte(dataentities.AgeableMobIndexIsBaby)
	if e.TypeName == "minecraft:piglin" {
		babyIndex = dataentities.PiglinIndexIsBaby
	}
	baby := metadataByte(e, babyIndex) != 0
	if e.TypeName == "minecraft:sniffer" && metadataInt(e, dataentities.SnifferIndexState) == 5 {
		height -= float32(0.4)
	}
	if baby {
		switch e.TypeName {
		case "minecraft:axolotl":
			width, height = 0.375, 0.21
		case "minecraft:chicken":
			width, height = 0.3, 0.4
		case "minecraft:cow", "minecraft:mooshroom":
			width, height = 0.45, 0.7
		case "minecraft:cat", "minecraft:ocelot":
			width, height = 0.3, 0.35
		case "minecraft:goat", "minecraft:sheep":
			width, height = 0.45, 0.65
		case "minecraft:pig":
			width, height = 0.45, 0.45
		case "minecraft:polar_bear":
			width, height = 0.7, 0.7
		case "minecraft:rabbit":
			width, height = 0.24, 0.4
		case "minecraft:squid", "minecraft:glow_squid":
			width, height = 0.5, 0.5
		case "minecraft:wolf":
			width, height = 0.3, 0.425
		case "minecraft:strider":
			width, height = 0.45, 0.85
		case "minecraft:hoglin", "minecraft:zoglin":
			width, height = 0.75, 0.85
		case "minecraft:piglin", "minecraft:zombie", "minecraft:husk", "minecraft:drowned", "minecraft:zombie_villager", "minecraft:zombified_piglin", "minecraft:villager":
			width, height = 0.49, 0.98
		case "minecraft:camel", "minecraft:camel_husk":
			width, height = 0.95, 1.4
		case "minecraft:armadillo", "minecraft:fox":
			width, height = width*float32(0.6), height*float32(0.6)
		case "minecraft:dolphin":
			width, height = width*float32(0.65), height*float32(0.65)
		case "minecraft:horse", "minecraft:skeleton_horse", "minecraft:zombie_horse":
			width, height = width*float32(0.7), height*float32(0.7)
		case "minecraft:happy_ghast":
			width, height = width*float32(0.2375), height*float32(0.2375)
		case "minecraft:turtle":
			width, height = width*float32(0.3), height*float32(0.3)
		case "minecraft:bee", "minecraft:donkey", "minecraft:mule", "minecraft:llama", "minecraft:trader_llama", "minecraft:nautilus", "minecraft:zombie_nautilus", "minecraft:panda", "minecraft:sniffer", "minecraft:frog", "minecraft:parrot", "minecraft:wandering_trader":
			width, height = width*float32(0.5), height*float32(0.5)
		}
	}
	if (e.TypeName == "minecraft:camel" || e.TypeName == "minecraft:camel_husk") && pose == 10 {
		if baby {
			height = 0.425
		} else {
			height -= float32(1.43)
		}
	}
	if e.TypeName == "minecraft:goat" && pose == 6 {
		width, height = width*float32(0.7), height*float32(0.7)
	}
	return width, height
}
