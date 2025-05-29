from django.contrib import admin
from .models import LLMModel, ImageGenModel

@admin.register(LLMModel)
class LLMModelAdmin(admin.ModelAdmin):
    list_display = ('name', 'slug', 'base_url', 'is_active', 'created_at', 'updated_at')
    list_filter = ('is_active', 'created_at')
    search_fields = ('name', 'slug', 'base_url')
    prepopulated_fields = {'slug': ('name',)}
    ordering = ('-created_at',)


@admin.register(ImageGenModel)
class ImageGenModelAdmin(admin.ModelAdmin):
    list_display = ('name', 'slug', 'service_type', 'base_url', 'is_active', 'created_at', 'updated_at')
    list_filter = ('service_type', 'is_active', 'created_at')
    search_fields = ('name', 'slug', 'base_url')
    ordering = ('-created_at',)
