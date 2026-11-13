package persona.core.rustcore.wire


data class Config(
    val manual_timers: Boolean
) {

    @Throws(com.novi.serde.SerializationError::class)
    fun serialize(serializer: com.novi.serde.Serializer) {
        serializer.increase_container_depth()
        serializer.serialize_bool(this.manual_timers)
        serializer.decrease_container_depth()
    }

    @Throws(com.novi.serde.SerializationError::class)
    fun bincodeSerialize(): ByteArray {
        val serializer = com.novi.bincode.BincodeSerializer()
        serialize(serializer)
        return serializer.get_bytes()
    }

    companion object {
        @Throws(com.novi.serde.DeserializationError::class)
        fun deserialize(deserializer: com.novi.serde.Deserializer): Config {
            deserializer.increase_container_depth()
            val manual_timers = deserializer.deserialize_bool()
            deserializer.decrease_container_depth()
            return Config(manual_timers)
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): Config {
            val deserializer = com.novi.bincode.BincodeDeserializer(input)
            val value = deserialize(deserializer)
            if (deserializer.get_buffer_offset() < input.size) {
                throw com.novi.serde.DeserializationError("Some input bytes were not read")
            }
            return value
        }
    }
}
