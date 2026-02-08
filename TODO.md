# TODO

- [ ] add method to check if a XYZ position is obstructed by a hitbox or entity from the bot's point of view (so we can't attack or store items through walls, for instance)
- [ ] do not attempt to store items in chests, trapped chests and shulker boxes that have a solid 1x1x1 block above them (other containers like barrels are fine and can be obstructed)
- [ ] item sorter example: when given an item in inventory, locate chests that have an item in item frame or sign with the item name attached on front (one item per line), pathfind to that chest and move the correct item into the chest
